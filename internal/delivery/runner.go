package delivery

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"discorddmbot/internal/config"
	"discorddmbot/internal/notify"
	"discorddmbot/internal/state"

	"github.com/bwmarrin/discordgo"
)

type Runner struct {
	session     *discordgo.Session
	configStore *config.Store
	store       *state.Store
	logger      *log.Logger
	notifier    *notify.DiscordWebhook
	notifyState map[string]string
}

func NewRunner(session *discordgo.Session, configStore *config.Store, statePath string, logger *log.Logger, notifier *notify.DiscordWebhook) *Runner {
	return &Runner{
		session:     session,
		configStore: configStore,
		store:       state.NewStore(statePath),
		logger:      logger,
		notifier:    notifier,
		notifyState: make(map[string]string),
	}
}

func (r *Runner) Run(ctx context.Context) error {
	fileState, err := r.store.Load()
	if err != nil {
		return err
	}

	delay := 15 * time.Second
	for {
		cfg, err := r.configStore.Load()
		if err != nil {
			r.logger.Printf("config load failed: %v", err)
		} else {
			delay = time.Duration(cfg.Runtime.PollIntervalSeconds) * time.Second
			if err := r.processDueDeliveries(cfg, fileState); err != nil {
				r.logger.Printf("delivery pass failed: %v", err)
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

func (r *Runner) processDueDeliveries(cfg *config.Config, fileState *state.FileState) error {
	location, err := time.LoadLocation(cfg.Runtime.Timezone)
	if err != nil {
		return fmt.Errorf("load timezone: %w", err)
	}

	now := time.Now().In(location)
	sendWindow := deliverySendWindow(cfg.Runtime.PollIntervalSeconds)
	for _, deliveryConfig := range cfg.Deliveries {
		scheduledDeliveries, err := deliveryConfig.Expand(location)
		if err != nil {
			r.logger.Printf("skip invalid delivery expansion id=%s user=%s: %v", deliveryConfig.ID, deliveryConfig.UserID, err)
			continue
		}

		for _, scheduledDelivery := range scheduledDeliveries {
			stateKey := scheduledDelivery.StateKey
			if record, alreadyDelivered := fileState.Deliveries[stateKey]; alreadyDelivered {
				if !now.Before(scheduledDelivery.ScheduledAt) {
					r.notifySkippedAlreadyDelivered(cfg, scheduledDelivery, record.DeliveredAtUTC)
				}
				continue
			}

			scheduledAt := scheduledDelivery.ScheduledAt

			if now.Before(scheduledAt) {
				continue
			}

			if !cfg.Runtime.SendMissedDeliveries && now.After(scheduledAt.Add(sendWindow)) {
				r.notifySkippedMissedWindow(cfg, scheduledDelivery, now, sendWindow)
				continue
			}

			message := scheduledDelivery.RenderMessage(cfg.Embed.DescriptionTemplate)
			guildID, err := EnsureUserInAnyGuild(r.session, cfg.Discord.GuildIDs, scheduledDelivery.UserID)
			if err != nil {
				r.logger.Printf("skip delivery for user=%s because they are not reachable in configured guilds=%v: %v", scheduledDelivery.UserID, cfg.Discord.GuildIDs, err)
				continue
			}

			if err := r.sendDM(cfg, scheduledDelivery, message, scheduledAt); err != nil {
				r.logger.Printf("send failed for user=%s delivery=%s: %v", scheduledDelivery.UserID, stateKey, err)
				r.notifyFailed(cfg, scheduledDelivery, err)
				continue
			}

			fileState.Deliveries[stateKey] = state.DeliveryRecord{
				UserID:         scheduledDelivery.UserID,
				Date:           scheduledDelivery.Date,
				Time:           scheduledDelivery.Time,
				DueDate:        scheduledDelivery.DueDate,
				DueTime:        scheduledDelivery.DueTime,
				ReminderName:   scheduledDelivery.ReminderName,
				Value:          scheduledDelivery.Value,
				Message:        message,
				DeliveredAtUTC: time.Now().UTC().Format(time.RFC3339),
			}

			if err := r.store.Save(fileState); err != nil {
				delete(fileState.Deliveries, stateKey)
				return fmt.Errorf("persist delivery state for %s: %w", stateKey, err)
			}

			delete(r.notifyState, "skip:"+stateKey)
			delete(r.notifyState, "fail:"+stateKey)
			r.logger.Printf("delivered dm to user=%s guild=%s scheduled_at=%s", scheduledDelivery.UserID, guildID, scheduledAt.Format(time.RFC3339))
			r.notifySent(cfg, scheduledDelivery, guildID)
		}
	}

	return nil
}

func (r *Runner) sendDM(cfg *config.Config, deliveryConfig config.ScheduledDelivery, message string, scheduledAt time.Time) error {
	embed, err := BuildDeliveryEmbed(r.session, cfg, deliveryConfig, message, scheduledAt)
	if err != nil {
		return err
	}

	return SendEmbedDM(r.session, deliveryConfig.UserID, embed)
}

func deliverySendWindow(pollIntervalSeconds int) time.Duration {
	window := time.Duration(pollIntervalSeconds) * time.Second
	if window < time.Minute {
		return time.Minute
	}

	return window
}

func (r *Runner) notifySent(cfg *config.Config, deliveryConfig config.ScheduledDelivery, guildID string) {
	if !cfg.Notifications.NotifySent || r.notifier == nil || !r.notifier.Enabled() {
		return
	}

	fields := []notify.Field{
		{Name: "User", Value: fmt.Sprintf("<@%s>", deliveryConfig.UserID), Inline: true},
		{Name: "Guild", Value: guildID, Inline: true},
		{Name: "Value", Value: deliveryConfig.Value, Inline: true},
		{Name: "Due", Value: dueValue(deliveryConfig), Inline: true},
	}
	if deliveryConfig.ReminderName != "" {
		fields = append(fields, notify.Field{Name: "Reminder", Value: deliveryConfig.ReminderName, Inline: true})
	}

	if err := r.notifier.Send("Reminder Sent", "A scheduled Discord DM was delivered successfully.", 0x2F855A, fields); err != nil {
		r.logger.Printf("webhook notify sent failed: %v", err)
	}
}

func (r *Runner) notifySkippedAlreadyDelivered(cfg *config.Config, deliveryConfig config.ScheduledDelivery, deliveredAtUTC string) {
	if !cfg.Notifications.NotifySkipped || r.notifier == nil || !r.notifier.Enabled() {
		return
	}

	key := "skip:already-delivered:" + deliveryConfig.StateKey
	if _, exists := r.notifyState[key]; exists {
		return
	}
	r.notifyState[key] = deliveredAtUTC

	fields := []notify.Field{
		{Name: "User", Value: fmt.Sprintf("<@%s>", deliveryConfig.UserID), Inline: true},
		{Name: "Value", Value: deliveryConfig.Value, Inline: true},
		{Name: "Due", Value: dueValue(deliveryConfig), Inline: true},
		{Name: "Reminder", Value: reminderValue(deliveryConfig), Inline: true},
		{Name: "Delivered At (UTC)", Value: deliveredAtUTC, Inline: true},
		{Name: "State Key", Value: safeStateKey(deliveryConfig.StateKey), Inline: false},
	}

	if err := r.notifier.Send(
		"Reminder Skipped",
		"This reminder was skipped because it is already marked as delivered in the local delivery state.",
		0xDD6B20,
		fields,
	); err != nil {
		r.logger.Printf("webhook notify skipped failed: %v", err)
	}
}

func (r *Runner) notifySkippedMissedWindow(cfg *config.Config, deliveryConfig config.ScheduledDelivery, now time.Time, sendWindow time.Duration) {
	if !cfg.Notifications.NotifySkipped || r.notifier == nil || !r.notifier.Enabled() {
		return
	}

	key := "skip:missed-window:" + deliveryConfig.StateKey
	reason := now.Format(time.RFC3339)
	if previous, exists := r.notifyState[key]; exists && previous == reason {
		return
	}
	r.notifyState[key] = reason

	fields := []notify.Field{
		{Name: "User", Value: fmt.Sprintf("<@%s>", deliveryConfig.UserID), Inline: true},
		{Name: "Value", Value: deliveryConfig.Value, Inline: true},
		{Name: "Due", Value: dueValue(deliveryConfig), Inline: true},
		{Name: "Reminder", Value: reminderValue(deliveryConfig), Inline: true},
		{Name: "Scheduled At", Value: deliveryConfig.ScheduledAt.Format(time.RFC3339), Inline: true},
		{Name: "Checked At", Value: now.Format(time.RFC3339), Inline: true},
		{Name: "Send Window", Value: sendWindow.String(), Inline: true},
		{Name: "State Key", Value: safeStateKey(deliveryConfig.StateKey), Inline: false},
	}

	if err := r.notifier.Send(
		"Reminder Skipped",
		"This reminder was skipped because the allowed send window had already expired and missed deliveries are disabled.",
		0xDD6B20,
		fields,
	); err != nil {
		r.logger.Printf("webhook notify skipped failed: %v", err)
	}
}

func (r *Runner) notifyFailed(cfg *config.Config, deliveryConfig config.ScheduledDelivery, sendErr error) {
	if !cfg.Notifications.NotifyFailed || r.notifier == nil || !r.notifier.Enabled() {
		return
	}

	key := "fail:" + deliveryConfig.StateKey
	reason := sendErr.Error()
	if previous, exists := r.notifyState[key]; exists && previous == reason {
		return
	}
	r.notifyState[key] = reason

	fields := []notify.Field{
		{Name: "User", Value: fmt.Sprintf("<@%s>", deliveryConfig.UserID), Inline: true},
		{Name: "Value", Value: deliveryConfig.Value, Inline: true},
		{Name: "Due", Value: dueValue(deliveryConfig), Inline: true},
		{Name: "Reminder", Value: reminderValue(deliveryConfig), Inline: true},
		{Name: "State Key", Value: safeStateKey(deliveryConfig.StateKey), Inline: false},
	}

	if err := r.notifier.Send("Reminder Send Failed", reason, 0xC53030, fields); err != nil {
		r.logger.Printf("webhook notify failed failed: %v", err)
	}
}

func dueValue(deliveryConfig config.ScheduledDelivery) string {
	if deliveryConfig.DueDate != "" {
		if deliveryConfig.DueTime != "" {
			return deliveryConfig.DueDate + " " + deliveryConfig.DueTime
		}
		return deliveryConfig.DueDate
	}

	return deliveryConfig.ScheduledAt.Format("2006-01-02 15:04 MST")
}

func reminderValue(deliveryConfig config.ScheduledDelivery) string {
	if deliveryConfig.ReminderName == "" {
		return "Not set"
	}

	return deliveryConfig.ReminderName
}

func safeStateKey(value string) string {
	replacer := strings.NewReplacer(":", ":\u200B", "`", "'")
	return "`" + replacer.Replace(value) + "`"
}
