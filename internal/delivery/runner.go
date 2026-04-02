package delivery

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"discorddmbot/internal/admin"
	"discorddmbot/internal/config"
	"discorddmbot/internal/state"

	"github.com/bwmarrin/discordgo"
)

type Runner struct {
	session     *discordgo.Session
	configStore *config.Store
	store       *state.Store
	logger      *log.Logger
	notifyState map[string]string
}

func NewRunner(session *discordgo.Session, configStore *config.Store, store *state.Store, logger *log.Logger) *Runner {
	return &Runner{
		session:     session,
		configStore: configStore,
		store:       store,
		logger:      logger,
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
		scheduledDeliveries, err := deliveryConfig.ExpandAt(location, now)
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
			r.notifySent(cfg, deliveryConfig, scheduledDelivery, guildID)
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

func (r *Runner) notifySent(cfg *config.Config, deliveryGroup config.Delivery, deliveryConfig config.ScheduledDelivery, guildID string) {
	if cfg.Discord.AdminChannelID == "" {
		return
	}

	description := notificationBody(
		deliveryConfig,
		"Status: Sent",
		fmt.Sprintf("Guild: `%s`", guildID),
	)

	embed, err := statusEmbed("Reminder Sent", description, 0x2F855A)
	if err != nil {
		r.logger.Printf("build admin sent embed failed: %v", err)
		return
	}

	var components []discordgo.MessageComponent
	if shouldOfferLateReminder(deliveryGroup, deliveryConfig) {
		components = admin.LateReminderComponents(deliveryConfig.DeliveryID, deliveryConfig.DueDate)
	}

	if err := admin.SendMessage(r.session, cfg.Discord.AdminChannelID, embed, components); err != nil {
		r.logger.Printf("admin notify sent failed: %v", err)
	}
}

func (r *Runner) notifySkippedAlreadyDelivered(cfg *config.Config, deliveryConfig config.ScheduledDelivery, deliveredAtUTC string) {
	if cfg.Discord.AdminChannelID == "" {
		return
	}

	key := "skip:already-delivered:" + deliveryConfig.StateKey
	if _, exists := r.notifyState[key]; exists {
		return
	}
	r.notifyState[key] = deliveredAtUTC

	embed, err := statusEmbed(
		"Reminder Skipped",
		notificationBody(
			deliveryConfig,
			"Status: Skipped",
			"Reason: Already marked as delivered in local state",
			fmt.Sprintf("Delivered At (UTC): `%s`", deliveredAtUTC),
		),
		0xDD6B20,
	)
	if err != nil {
		r.logger.Printf("build admin skipped embed failed: %v", err)
		return
	}

	if err := admin.SendMessage(r.session, cfg.Discord.AdminChannelID, embed, nil); err != nil {
		r.logger.Printf("admin notify skipped failed: %v", err)
	}
}

func (r *Runner) notifySkippedMissedWindow(cfg *config.Config, deliveryConfig config.ScheduledDelivery, now time.Time, sendWindow time.Duration) {
	if cfg.Discord.AdminChannelID == "" {
		return
	}

	key := "skip:missed-window:" + deliveryConfig.StateKey
	reason := now.Format(time.RFC3339)
	if previous, exists := r.notifyState[key]; exists && previous == reason {
		return
	}
	r.notifyState[key] = reason

	embed, err := statusEmbed(
		"Reminder Skipped",
		notificationBody(
			deliveryConfig,
			"Status: Skipped",
			"Reason: Send window expired while missed deliveries are disabled",
			fmt.Sprintf("Scheduled At: `%s`", deliveryConfig.ScheduledAt.Format(time.RFC3339)),
			fmt.Sprintf("Checked At: `%s`", now.Format(time.RFC3339)),
			fmt.Sprintf("Send Window: `%s`", sendWindow),
		),
		0xDD6B20,
	)
	if err != nil {
		r.logger.Printf("build admin skipped embed failed: %v", err)
		return
	}

	if err := admin.SendMessage(r.session, cfg.Discord.AdminChannelID, embed, nil); err != nil {
		r.logger.Printf("admin notify skipped failed: %v", err)
	}
}

func (r *Runner) notifyFailed(cfg *config.Config, deliveryConfig config.ScheduledDelivery, sendErr error) {
	if cfg.Discord.AdminChannelID == "" {
		return
	}

	key := "fail:" + deliveryConfig.StateKey
	reason := sendErr.Error()
	if previous, exists := r.notifyState[key]; exists && previous == reason {
		return
	}
	r.notifyState[key] = reason

	embed, err := statusEmbed(
		"Reminder Send Failed",
		notificationBody(
			deliveryConfig,
			"Status: Failed",
			fmt.Sprintf("Reason: `%s`", reason),
		),
		0xC53030,
	)
	if err != nil {
		r.logger.Printf("build admin failed embed failed: %v", err)
		return
	}

	if err := admin.SendMessage(r.session, cfg.Discord.AdminChannelID, embed, nil); err != nil {
		r.logger.Printf("admin notify failed failed: %v", err)
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

func notificationBody(deliveryConfig config.ScheduledDelivery, extraLines ...string) string {
	lines := []string{
		fmt.Sprintf("User: %s", deliveryConfig.UserMention()),
		fmt.Sprintf("Value: **%s**", deliveryConfig.Value),
		fmt.Sprintf("Reminder: **%s**", reminderValue(deliveryConfig)),
		fmt.Sprintf("Due: **%s**", dueValue(deliveryConfig)),
	}

	for _, line := range extraLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func statusEmbed(title, description string, color int) (*discordgo.MessageEmbed, error) {
	return &discordgo.MessageEmbed{
		Title:       title,
		Description: description,
		Color:       color,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func shouldOfferLateReminder(deliveryGroup config.Delivery, deliveryConfig config.ScheduledDelivery) bool {
	if deliveryConfig.ReminderID != "final" {
		return false
	}
	if strings.TrimSpace(deliveryConfig.DeliveryID) == "" {
		return false
	}
	_, exists := deliveryGroup.ReminderByID("late")
	return exists
}
