package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
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
	lastConfig  *configSnapshot
}

type configSnapshot struct {
	hash string
	cfg  *config.Config
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
			} else {
				r.notifyConfigApplied(cfg)
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
			if _, alreadyDelivered := fileState.Deliveries[stateKey]; alreadyDelivered {
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

func (r *Runner) notifyConfigApplied(cfg *config.Config) {
	if cfg == nil {
		return
	}

	hash := configFingerprint(cfg)
	if r.lastConfig == nil {
		r.lastConfig = &configSnapshot{hash: hash, cfg: cloneConfig(cfg)}
		return
	}

	if r.lastConfig.hash == hash {
		return
	}

	previous := r.lastConfig.cfg
	r.lastConfig = &configSnapshot{hash: hash, cfg: cloneConfig(cfg)}

	if cfg.Discord.AdminChannelID == "" {
		return
	}

	summary := buildConfigChangeSummary(previous, cfg)
	color, err := config.ParseHexColor(cfg.Embed.ConfigChangeColor)
	if err != nil {
		r.logger.Printf("config applied color parse failed: %v", err)
		return
	}

	embed := admin.StatusEmbed(
		"Config Changes Applied",
		summary,
		color,
	)

	if err := admin.SendMessage(r.session, cfg.Discord.AdminChannelID, "", embed, nil); err != nil {
		r.logger.Printf("admin notify config applied failed: %v", err)
	}
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

	message := deliveryConfig.RenderMessage(cfg.Embed.DescriptionTemplate)
	embed, err := BuildDeliveryEmbed(r.session, cfg, deliveryConfig, message, deliveryConfig.ScheduledAt)
	if err != nil {
		r.logger.Printf("build admin sent embed failed: %v", err)
		return
	}

	var components []discordgo.MessageComponent
	if shouldOfferLateReminder(deliveryGroup, deliveryConfig) {
		components = admin.LateReminderComponents(deliveryConfig.DeliveryID, deliveryConfig.DueDate)
	}

	content := buildAdminStatusContent("Reminder Delivered", deliveryConfig,
		fmt.Sprintf("Guild: `%s`", guildID),
	)
	if err := admin.SendMessage(r.session, cfg.Discord.AdminChannelID, content, embed, components); err != nil {
		r.logger.Printf("admin notify sent failed: %v", err)
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

	message := deliveryConfig.RenderMessage(cfg.Embed.DescriptionTemplate)
	embed, err := BuildDeliveryEmbed(r.session, cfg, deliveryConfig, message, deliveryConfig.ScheduledAt)
	if err != nil {
		r.logger.Printf("build admin skipped embed failed: %v", err)
		return
	}
	embed, err = admin.CloneEmbedWithColor(embed, "#C53030")
	if err != nil {
		r.logger.Printf("recolor admin skipped embed failed: %v", err)
		return
	}

	content := buildAdminStatusContent(
		"Reminder Skipped",
		deliveryConfig,
		fmt.Sprintf("Reason: send window expired at `%s`", now.Format(time.RFC3339)),
		fmt.Sprintf("Window: `%s`", sendWindow),
	)
	if err := admin.SendMessage(r.session, cfg.Discord.AdminChannelID, content, embed, nil); err != nil {
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

	message := deliveryConfig.RenderMessage(cfg.Embed.DescriptionTemplate)
	embed, err := BuildDeliveryEmbed(r.session, cfg, deliveryConfig, message, deliveryConfig.ScheduledAt)
	if err != nil {
		r.logger.Printf("build admin failed embed failed: %v", err)
		return
	}

	content := buildAdminStatusContent(
		"Reminder Failed",
		deliveryConfig,
		fmt.Sprintf("Reason: `%s`", reason),
	)
	if err := admin.SendMessage(r.session, cfg.Discord.AdminChannelID, content, embed, nil); err != nil {
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

func buildAdminStatusContent(title string, deliveryConfig config.ScheduledDelivery, extraLines ...string) string {
	lines := []string{
		"**" + strings.TrimSpace(title) + "**",
		fmt.Sprintf("User: %s", deliveryConfig.UserMention()),
		fmt.Sprintf("Reminder: %s", reminderValue(deliveryConfig)),
		fmt.Sprintf("Due: %s", dueValue(deliveryConfig)),
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

func shouldOfferLateReminder(deliveryGroup config.Delivery, deliveryConfig config.ScheduledDelivery) bool {
	if deliveryConfig.ReminderID != "due" {
		return false
	}
	if strings.TrimSpace(deliveryConfig.DeliveryID) == "" {
		return false
	}
	_, exists := deliveryGroup.ReminderByID("late")
	return exists
}

func configFingerprint(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}

	h := sha256.New()
	writeFingerprint := func(parts ...string) {
		for _, part := range parts {
			_, _ = h.Write([]byte(part))
			_, _ = h.Write([]byte{0})
		}
	}

	writeFingerprint(
		cfg.Discord.AdminChannelID,
		strings.Join(cfg.Discord.GuildIDs, ","),
		strings.Join(cfg.Discord.AllowedRoleIDs, ","),
		cfg.Runtime.Timezone,
		fmt.Sprintf("%d", cfg.Runtime.PollIntervalSeconds),
		fmt.Sprintf("%t", cfg.Runtime.SendMissedDeliveries),
		cfg.Runtime.StatePath,
		cfg.Embed.Title,
		cfg.Embed.DescriptionTemplate,
		cfg.Embed.Footer,
		cfg.Embed.Color,
		cfg.Embed.ConfigChangeColor,
		cfg.Embed.InitialColor,
		cfg.Embed.FinalColor,
		cfg.Embed.DueColor,
		cfg.Embed.LateColor,
		cfg.Embed.OneOffColor,
	)

	for _, deliveryCfg := range cfg.Deliveries {
		writeFingerprint(deliveryDigest(deliveryCfg))
	}

	return hex.EncodeToString(h.Sum(nil))
}

func deliveryDigest(deliveryCfg config.Delivery) string {
	parts := []string{
		deliveryCfg.ID,
		deliveryCfg.UserID,
		deliveryCfg.Date,
		deliveryCfg.Time,
		deliveryCfg.Message,
		deliveryCfg.Value,
		deliveryCfg.DueDate,
		deliveryCfg.DueTime,
		deliveryCfg.Frequency,
	}

	for _, reminder := range deliveryCfg.Reminders {
		parts = append(parts,
			reminder.ID,
			reminder.Name,
			reminder.Title,
			fmt.Sprintf("%d", reminder.DaysBeforeDue),
			reminder.Time,
			reminder.Message,
		)
	}

	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func buildConfigChangeSummary(previous, current *config.Config) string {
	if previous == nil || current == nil {
		return "The running bot reloaded the config file."
	}

	lines := []string{"The running bot detected and applied config changes from disk."}

	if previous.Runtime.Timezone != current.Runtime.Timezone ||
		previous.Runtime.PollIntervalSeconds != current.Runtime.PollIntervalSeconds ||
		previous.Runtime.SendMissedDeliveries != current.Runtime.SendMissedDeliveries ||
		previous.Runtime.StatePath != current.Runtime.StatePath {
		lines = append(lines, "- Runtime settings updated")
	}

	if previous.Embed != current.Embed {
		lines = append(lines, "- Embed settings updated")
	}

	if previous.Discord.AdminChannelID != current.Discord.AdminChannelID ||
		!equalStringSlices(previous.Discord.GuildIDs, current.Discord.GuildIDs) ||
		!equalStringSlices(previous.Discord.AllowedRoleIDs, current.Discord.AllowedRoleIDs) {
		lines = append(lines, "- Discord scope or admin channel settings updated")
	}

	added, removed, updated := diffDeliveries(previous.Deliveries, current.Deliveries)
	if len(added) > 0 {
		lines = append(lines, fmt.Sprintf("- Added deliveries: %s", strings.Join(added, ", ")))
	}
	if len(updated) > 0 {
		lines = append(lines, fmt.Sprintf("- Updated deliveries: %s", strings.Join(updated, ", ")))
	}
	if len(removed) > 0 {
		lines = append(lines, fmt.Sprintf("- Removed deliveries: %s", strings.Join(removed, ", ")))
	}
	if len(lines) == 1 {
		lines = append(lines, "- No high-level summary was generated, but the config fingerprint changed")
	}

	return strings.Join(lines, "\n")
}

func diffDeliveries(previous, current []config.Delivery) ([]string, []string, []string) {
	previousMap := make(map[string]config.Delivery, len(previous))
	currentMap := make(map[string]config.Delivery, len(current))

	for index, deliveryCfg := range previous {
		previousMap[deliveryLabel(deliveryCfg, index)] = deliveryCfg
	}
	for index, deliveryCfg := range current {
		currentMap[deliveryLabel(deliveryCfg, index)] = deliveryCfg
	}

	var added []string
	var removed []string
	var updated []string

	for label, currentDelivery := range currentMap {
		previousDelivery, exists := previousMap[label]
		if !exists {
			added = append(added, label)
			continue
		}
		if deliveryDigest(previousDelivery) != deliveryDigest(currentDelivery) {
			updated = append(updated, label)
		}
	}

	for label := range previousMap {
		if _, exists := currentMap[label]; !exists {
			removed = append(removed, label)
		}
	}

	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(updated)
	return added, removed, updated
}

func deliveryLabel(deliveryCfg config.Delivery, index int) string {
	if strings.TrimSpace(deliveryCfg.ID) != "" {
		return deliveryCfg.ID
	}
	if strings.TrimSpace(deliveryCfg.DueDate) != "" {
		return fmt.Sprintf("user:%s due:%s #%d", deliveryCfg.UserID, deliveryCfg.DueDate, index+1)
	}
	return fmt.Sprintf("user:%s at:%s %s #%d", deliveryCfg.UserID, deliveryCfg.Date, deliveryCfg.Time, index+1)
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}

	cloned := *cfg
	cloned.Discord.GuildIDs = append([]string(nil), cfg.Discord.GuildIDs...)
	cloned.Discord.AllowedRoleIDs = append([]string(nil), cfg.Discord.AllowedRoleIDs...)
	cloned.Deliveries = make([]config.Delivery, len(cfg.Deliveries))
	for index, deliveryCfg := range cfg.Deliveries {
		cloned.Deliveries[index] = deliveryCfg
		cloned.Deliveries[index].Reminders = append([]config.Reminder(nil), deliveryCfg.Reminders...)
	}

	return &cloned
}
