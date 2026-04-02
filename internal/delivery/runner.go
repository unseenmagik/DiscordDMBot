package delivery

import (
	"context"
	"fmt"
	"log"
	"time"

	"discorddmbot/internal/config"
	"discorddmbot/internal/state"

	"github.com/bwmarrin/discordgo"
)

type Runner struct {
	session     *discordgo.Session
	configStore *config.Store
	store       *state.Store
	logger      *log.Logger
}

func NewRunner(session *discordgo.Session, configStore *config.Store, statePath string, logger *log.Logger) *Runner {
	return &Runner{
		session:     session,
		configStore: configStore,
		store:       state.NewStore(statePath),
		logger:      logger,
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
			if _, alreadyDelivered := fileState.Deliveries[stateKey]; alreadyDelivered {
				continue
			}

			scheduledAt := scheduledDelivery.ScheduledAt

			if now.Before(scheduledAt) {
				continue
			}

			if !cfg.Runtime.SendMissedDeliveries && now.After(scheduledAt.Add(sendWindow)) {
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

			r.logger.Printf("delivered dm to user=%s guild=%s scheduled_at=%s", scheduledDelivery.UserID, guildID, scheduledAt.Format(time.RFC3339))
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
