package commands

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"discorddmbot/internal/admin"
	"discorddmbot/internal/config"
	"discorddmbot/internal/delivery"
	"discorddmbot/internal/state"

	"github.com/bwmarrin/discordgo"
)

const (
	commandSendNow         = "send-now"
	commandReminderResend  = "reminder-resend"
	commandScheduleAdd     = "schedule-add"
	commandScheduleEdit    = "schedule-edit"
	commandScheduleListIDs = "schedule-list-ids"
	commandScheduleRemove  = "schedule-remove"
	commandStateClear      = "state-clear"
	commandScheduleView    = "schedule-view"
	maxScheduleEmbeds      = 10
	maxFieldsPerEmbed      = 5
)

type Service struct {
	session        *discordgo.Session
	configStore    *config.Store
	stateStore     *state.Store
	logger         *log.Logger
	guildIDs       map[string]struct{}
	guildIDList    []string
	allowedRoleIDs map[string]struct{}
}

type userFacingError struct {
	message string
}

func (e userFacingError) Error() string {
	return e.message
}

type deferredInteractionError struct {
	err error
}

func (e deferredInteractionError) Error() string {
	return e.err.Error()
}

func (e deferredInteractionError) Unwrap() error {
	return e.err
}

func NewService(session *discordgo.Session, configStore *config.Store, stateStore *state.Store, logger *log.Logger, discordConfig config.Discord) *Service {
	allowedRoleIDs := make(map[string]struct{}, len(discordConfig.AllowedRoleIDs))
	for _, roleID := range discordConfig.AllowedRoleIDs {
		allowedRoleIDs[roleID] = struct{}{}
	}

	guildIDs := make(map[string]struct{}, len(discordConfig.GuildIDs))
	for _, guildID := range discordConfig.GuildIDs {
		guildIDs[guildID] = struct{}{}
	}

	service := &Service{
		session:        session,
		configStore:    configStore,
		stateStore:     stateStore,
		logger:         logger,
		guildIDs:       guildIDs,
		guildIDList:    append([]string(nil), discordConfig.GuildIDs...),
		allowedRoleIDs: allowedRoleIDs,
	}

	session.AddHandler(service.onInteractionCreate)
	return service
}

func (s *Service) Register(applicationID string) error {
	for _, guildID := range s.guildIDList {
		if _, err := s.session.ApplicationCommandBulkOverwrite(applicationID, guildID, applicationCommands()); err != nil {
			return fmt.Errorf("register application commands for guild %s: %w", guildID, err)
		}
	}

	return nil
}

func (s *Service) onInteractionCreate(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
	if interaction.Type != discordgo.InteractionApplicationCommand && interaction.Type != discordgo.InteractionMessageComponent {
		return
	}

	if !s.guildAllowed(interaction.GuildID) {
		s.respondError(interaction.Interaction, "This bot is only configured to accept commands in the configured guild.")
		return
	}

	if !s.memberHasAllowedRole(interaction.Member) {
		s.respondError(interaction.Interaction, "You do not have the required role to use this bot.")
		return
	}

	var err error
	switch interaction.Type {
	case discordgo.InteractionApplicationCommand:
		switch interaction.ApplicationCommandData().Name {
		case commandSendNow:
			err = s.handleSendNow(interaction)
		case commandReminderResend:
			err = s.handleReminderResend(interaction)
		case commandScheduleAdd:
			err = s.handleScheduleAdd(interaction)
		case commandScheduleEdit:
			err = s.handleScheduleEdit(interaction)
		case commandScheduleListIDs:
			err = s.handleScheduleListIDs(interaction)
		case commandScheduleRemove:
			err = s.handleScheduleRemove(interaction)
		case commandStateClear:
			err = s.handleStateClear(interaction)
		case commandScheduleView:
			err = s.handleScheduleView(interaction)
		default:
			err = s.respondError(interaction.Interaction, "Unknown command.")
		}
	case discordgo.InteractionMessageComponent:
		err = s.handleComponent(interaction)
	default:
		return
	}

	if err != nil {
		name := "component"
		if interaction.Type == discordgo.InteractionApplicationCommand {
			name = interaction.ApplicationCommandData().Name
		}
		s.logger.Printf("interaction %s failed: %v", name, err)
		var userErr userFacingError
		responseMessage := "The command could not be completed. Check the bot logs for details."
		if errors.As(err, &userErr) {
			responseMessage = userErr.message
		}
		var deferredErr deferredInteractionError
		var responseErr error
		if errors.As(err, &deferredErr) {
			responseErr = s.editDeferredText(interaction.Interaction, responseMessage)
		} else {
			responseErr = s.respondError(interaction.Interaction, responseMessage)
		}
		if responseErr != nil {
			s.logger.Printf("failed to send interaction error response: %v", responseErr)
		}
	}
}

func (s *Service) handleSendNow(interaction *discordgo.InteractionCreate) error {
	options := optionsByName(interaction.ApplicationCommandData().Options)

	user := options["user"].UserValue(nil)
	if user == nil {
		return userFacingError{message: "A valid Discord user is required."}
	}

	cfg, err := s.configStore.Load()
	if err != nil {
		return err
	}

	if _, err := delivery.EnsureUserInAnyGuild(s.session, cfg.Discord.GuildIDs, user.ID); err != nil {
		return userFacingError{message: fmt.Sprintf("<@%s> is not a member of any configured guild.", user.ID)}
	}

	location, err := time.LoadLocation(cfg.Runtime.Timezone)
	if err != nil {
		return err
	}

	now := time.Now().In(location)
	dueDate := optionalString(options, "due_date")
	dueTime := optionalString(options, "due_time")
	if dueTime != "" && dueDate == "" {
		return userFacingError{message: "due_time can only be used when due_date is also provided."}
	}
	if dueDate != "" {
		if _, err := time.ParseInLocation("2006-01-02", dueDate, location); err != nil {
			return userFacingError{message: "due_date must use YYYY-MM-DD format."}
		}
	}
	if dueTime != "" {
		if _, err := time.ParseInLocation("15:04", dueTime, location); err != nil {
			return userFacingError{message: "due_time must use HH:MM 24-hour format."}
		}
	}

	deliveryConfig := config.ScheduledDelivery{
		UserID:      user.ID,
		Date:        now.Format("2006-01-02"),
		Time:        now.Format("15:04"),
		Value:       strings.TrimSpace(options["value"].StringValue()),
		Message:     optionalString(options, "message"),
		DueDate:     dueDate,
		DueTime:     dueTime,
		ScheduledAt: now,
	}

	message := deliveryConfig.RenderMessage(cfg.Embed.DescriptionTemplate)
	embed, err := delivery.BuildDeliveryEmbed(s.session, cfg, deliveryConfig, message, now)
	if err != nil {
		return err
	}

	if err := delivery.SendEmbedDM(s.session, deliveryConfig.UserID, embed); err != nil {
		return userFacingError{message: fmt.Sprintf("I could not DM <@%s>. They may have DMs disabled or not share a server with the bot.", deliveryConfig.UserID)}
	}

	return s.respondEmbeds(interaction.Interaction, fmt.Sprintf("Sent the embed to <@%s>.", deliveryConfig.UserID), []*discordgo.MessageEmbed{embed})
}

func (s *Service) handleReminderResend(interaction *discordgo.InteractionCreate) error {
	options := optionsByName(interaction.ApplicationCommandData().Options)

	deliveryID := requiredString(options, "id")
	reminderID := requiredString(options, "reminder_id")
	dueDate := optionalString(options, "due_date")

	cfg, err := s.configStore.Load()
	if err != nil {
		return err
	}

	deliveryGroup, found := findDeliveryByID(cfg.Deliveries, deliveryID)
	if !found {
		return userFacingError{message: fmt.Sprintf("Delivery %q was not found in the config.", deliveryID)}
	}

	if strings.TrimSpace(deliveryGroup.DueDate) == "" || len(deliveryGroup.Reminders) == 0 {
		return userFacingError{message: "Manual resend only supports reminder-based delivery groups."}
	}

	reminder, found := deliveryGroup.ReminderByID(reminderID)
	if !found {
		return userFacingError{message: fmt.Sprintf("Reminder %q was not found under delivery %q.", reminderID, deliveryID)}
	}

	location, err := time.LoadLocation(cfg.Runtime.Timezone)
	if err != nil {
		return err
	}

	if dueDate != "" {
		if _, err := time.ParseInLocation("2006-01-02", dueDate, location); err != nil {
			return userFacingError{message: "due_date must use YYYY-MM-DD format."}
		}
	} else {
		if frequencyLabel(deliveryGroup.Frequency) != "once" {
			return userFacingError{message: "due_date is required when manually resending a recurring delivery."}
		}
		dueDate = deliveryGroup.DueDate
	}

	if _, err := delivery.EnsureUserInAnyGuild(s.session, cfg.Discord.GuildIDs, deliveryGroup.UserID); err != nil {
		return userFacingError{message: fmt.Sprintf("<@%s> is not a member of any configured guild.", deliveryGroup.UserID)}
	}

	now := time.Now().In(location)
	resendDelivery := config.ScheduledDelivery{
		DeliveryID:    deliveryGroup.ID,
		UserID:        deliveryGroup.UserID,
		Value:         deliveryGroup.Value,
		Message:       reminder.Message,
		ScheduledAt:   now,
		Date:          now.Format("2006-01-02"),
		Time:          now.Format("15:04"),
		DueDate:       dueDate,
		DueTime:       deliveryGroup.DueTime,
		Frequency:     frequencyLabel(deliveryGroup.Frequency),
		ReminderID:    reminder.ID,
		ReminderName:  reminder.Name,
		Title:         reminder.Title,
		DaysBeforeDue: reminder.DaysBeforeDue,
	}

	message := resendDelivery.RenderMessage(cfg.Embed.DescriptionTemplate)
	embed, err := delivery.BuildDeliveryEmbed(s.session, cfg, resendDelivery, message, now)
	if err != nil {
		return err
	}

	if err := delivery.SendEmbedDM(s.session, resendDelivery.UserID, embed); err != nil {
		return userFacingError{message: fmt.Sprintf("I could not DM <@%s> with the manual resend.", resendDelivery.UserID)}
	}

	if cfg.Discord.AdminChannelID != "" {
		content := buildAdminManualResendContent(resendDelivery, reminderID)
		if err := admin.SendMessage(s.session, cfg.Discord.AdminChannelID, content, embed, nil); err != nil {
			s.logger.Printf("send admin manual resend status failed: %v", err)
		}
	}

	return s.respondEmbeds(interaction.Interaction, fmt.Sprintf("Manually resent the %s reminder to <@%s>.", valueOrFallback(resendDelivery.ReminderName, reminderID), resendDelivery.UserID), []*discordgo.MessageEmbed{embed})
}

func (s *Service) handleScheduleAdd(interaction *discordgo.InteractionCreate) error {
	options := optionsByName(interaction.ApplicationCommandData().Options)

	user := options["user"].UserValue(nil)
	if user == nil {
		return userFacingError{message: "A valid Discord user is required."}
	}

	newDelivery := config.Delivery{
		ID:        optionalString(options, "id"),
		UserID:    user.ID,
		DueDate:   strings.TrimSpace(options["due_date"].StringValue()),
		DueTime:   optionalString(options, "due_time"),
		Frequency: optionalString(options, "frequency"),
		Value:     strings.TrimSpace(options["value"].StringValue()),
		Reminders: []config.Reminder{
			{
				ID:            "initial",
				Name:          "Initial Reminder",
				DaysBeforeDue: optionalInt(options, "initial_days_before", 3),
				Time:          requiredString(options, "initial_time"),
				Message:       requiredString(options, "initial_message"),
			},
			{
				ID:            "final",
				Name:          "Final Reminder",
				DaysBeforeDue: optionalInt(options, "final_days_before", 1),
				Time:          requiredString(options, "final_time"),
				Message:       requiredString(options, "final_message"),
			},
		},
	}

	currentConfig, err := s.configStore.Load()
	if err != nil {
		return err
	}

	if _, err := delivery.EnsureUserInAnyGuild(s.session, currentConfig.Discord.GuildIDs, user.ID); err != nil {
		return userFacingError{message: fmt.Sprintf("<@%s> is not a member of any configured guild.", user.ID)}
	}

	cfg, err := s.configStore.AddDelivery(newDelivery)
	if err != nil {
		return userFacingError{message: err.Error()}
	}

	location, err := time.LoadLocation(cfg.Runtime.Timezone)
	if err != nil {
		return err
	}

	expandedDeliveries, err := newDelivery.ExpandAt(location, time.Now().In(location))
	if err != nil {
		return err
	}

	color, err := config.ParseHexColor(cfg.Embed.Color)
	if err != nil {
		return err
	}

	confirmation := &discordgo.MessageEmbed{
		Title:       "Schedule Saved",
		Description: "The schedule was saved to `config/config.toml` and will be picked up automatically on the next poll.",
		Color:       color,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "User",
				Value:  fmt.Sprintf("<@%s>", newDelivery.UserID),
				Inline: true,
			},
			{
				Name:   "Value",
				Value:  newDelivery.Value,
				Inline: true,
			},
			{
				Name:   "Payment Due",
				Value:  dueLine(newDelivery),
				Inline: false,
			},
			{
				Name:   "Frequency",
				Value:  frequencyLabel(newDelivery.Frequency),
				Inline: true,
			},
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	if newDelivery.ID != "" {
		confirmation.Fields = append(confirmation.Fields, &discordgo.MessageEmbedField{
			Name:   "ID",
			Value:  newDelivery.ID,
			Inline: true,
		})
	}

	for _, scheduledDelivery := range expandedDeliveries {
		confirmation.Fields = append(confirmation.Fields, &discordgo.MessageEmbedField{
			Name: fmt.Sprintf("%s", scheduledDelivery.ReminderName),
			Value: fmt.Sprintf(
				"When: %s\nDays Before Due: %d",
				scheduledDelivery.ScheduledAt.Format("2006-01-02 15:04 MST"),
				scheduledDelivery.DaysBeforeDue,
			),
			Inline: false,
		})
	}

	return s.respondEmbeds(interaction.Interaction, "", []*discordgo.MessageEmbed{confirmation})
}

func (s *Service) handleScheduleView(interaction *discordgo.InteractionCreate) error {
	options := optionsByName(interaction.ApplicationCommandData().Options)

	cfg, err := s.configStore.Load()
	if err != nil {
		return err
	}

	filterID := optionalString(options, "id")
	if filterID != "" {
		if _, found := findDeliveryByID(cfg.Deliveries, filterID); !found {
			return userFacingError{message: fmt.Sprintf("Delivery %q was not found in the config.", filterID)}
		}
	}

	embeds, err := buildScheduleEmbeds(cfg, filterID)
	if err != nil {
		return err
	}

	return s.respondEmbeds(interaction.Interaction, "", embeds)
}

func (s *Service) handleScheduleListIDs(interaction *discordgo.InteractionCreate) error {
	cfg, err := s.configStore.Load()
	if err != nil {
		return err
	}

	embeds, err := buildScheduleIDEmbeds(cfg)
	if err != nil {
		return err
	}

	return s.respondEmbeds(interaction.Interaction, "", embeds)
}

func (s *Service) handleScheduleEdit(interaction *discordgo.InteractionCreate) error {
	options := optionsByName(interaction.ApplicationCommandData().Options)

	deliveryID := requiredString(options, "id")
	if deliveryID == "" {
		return userFacingError{message: "A delivery id is required."}
	}

	currentConfig, err := s.configStore.Load()
	if err != nil {
		return err
	}

	location, err := time.LoadLocation(currentConfig.Runtime.Timezone)
	if err != nil {
		return err
	}

	var editedDelivery config.Delivery
	cfg, err := s.configStore.UpdateDelivery(deliveryID, func(deliveryConfig *config.Delivery) error {
		if userOption, exists := options["user"]; exists {
			user := userOption.UserValue(nil)
			if user == nil {
				return userFacingError{message: "A valid Discord user is required."}
			}
			if _, err := delivery.EnsureUserInAnyGuild(s.session, currentConfig.Discord.GuildIDs, user.ID); err != nil {
				return userFacingError{message: fmt.Sprintf("<@%s> is not a member of any configured guild.", user.ID)}
			}
			deliveryConfig.UserID = user.ID
		}

		if value := optionalString(options, "due_date"); value != "" {
			deliveryConfig.DueDate = value
		}
		if _, exists := options["due_time"]; exists {
			deliveryConfig.DueTime = optionalString(options, "due_time")
		}
		if value := optionalString(options, "frequency"); value != "" {
			deliveryConfig.Frequency = value
		}
		if value := optionalString(options, "value"); value != "" {
			deliveryConfig.Value = value
		}

		applyReminderEdit(deliveryConfig, "initial", reminderEdit{
			Title:         optionalString(options, "initial_title"),
			Time:          optionalString(options, "initial_time"),
			Message:       optionalString(options, "initial_message"),
			DaysBeforeDue: optionalOptionalInt(options, "initial_days_before"),
			DefaultName:   "Initial Reminder",
		})
		applyReminderEdit(deliveryConfig, "final", reminderEdit{
			Title:         optionalString(options, "final_title"),
			Time:          optionalString(options, "final_time"),
			Message:       optionalString(options, "final_message"),
			DaysBeforeDue: optionalOptionalInt(options, "final_days_before"),
			DefaultName:   "Final Reminder",
		})
		applyReminderEdit(deliveryConfig, "due", reminderEdit{
			Title:         optionalString(options, "due_title"),
			Time:          optionalString(options, "due_time_reminder"),
			Message:       optionalString(options, "due_message"),
			DaysBeforeDue: optionalOptionalInt(options, "due_days_before"),
			DefaultName:   "Due Reminder",
		})
		applyReminderEdit(deliveryConfig, "late", reminderEdit{
			Title:       optionalString(options, "late_title"),
			Message:     optionalString(options, "late_message"),
			DefaultName: "Late Reminder",
		})

		editedDelivery = *deliveryConfig
		return nil
	})
	if err != nil {
		return userFacingError{message: err.Error()}
	}

	expandedDeliveries, err := editedDelivery.ExpandAt(location, time.Now().In(location))
	if err != nil {
		return err
	}

	color, err := config.ParseHexColor(cfg.Embed.Color)
	if err != nil {
		return err
	}

	confirmation := &discordgo.MessageEmbed{
		Title:       "Schedule Updated",
		Description: "The schedule was updated in `config/config.toml`.",
		Color:       color,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "ID", Value: editedDelivery.ID, Inline: true},
			{Name: "User", Value: fmt.Sprintf("<@%s>", editedDelivery.UserID), Inline: true},
			{Name: "Value", Value: editedDelivery.Value, Inline: true},
			{Name: "Payment Due", Value: dueLine(editedDelivery), Inline: false},
			{Name: "Frequency", Value: frequencyLabel(editedDelivery.Frequency), Inline: true},
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	for _, scheduledDelivery := range expandedDeliveries {
		confirmation.Fields = append(confirmation.Fields, &discordgo.MessageEmbedField{
			Name: fmt.Sprintf("%s", valueOrFallback(scheduledDelivery.ReminderName, "Reminder")),
			Value: fmt.Sprintf(
				"When: %s\nDays Before Due: %d",
				scheduledDelivery.ScheduledAt.Format("2006-01-02 15:04 MST"),
				scheduledDelivery.DaysBeforeDue,
			),
			Inline: false,
		})
	}

	return s.respondEmbeds(interaction.Interaction, "", []*discordgo.MessageEmbed{confirmation})
}

func (s *Service) handleScheduleRemove(interaction *discordgo.InteractionCreate) error {
	options := optionsByName(interaction.ApplicationCommandData().Options)

	deliveryID := requiredString(options, "id")
	if deliveryID == "" {
		return userFacingError{message: "A delivery id is required."}
	}

	cfg, removedDelivery, err := s.configStore.RemoveDelivery(deliveryID)
	if err != nil {
		return userFacingError{message: err.Error()}
	}

	clearedStateEntries, err := s.stateStore.ClearForDeliveryID(deliveryID)
	if err != nil {
		return err
	}

	color, err := config.ParseHexColor(cfg.Embed.Color)
	if err != nil {
		return err
	}

	confirmation := &discordgo.MessageEmbed{
		Title:       "Schedule Removed",
		Description: "The schedule was removed from `config/config.toml`. The scheduler will stop using it on the next poll.",
		Color:       color,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "ID", Value: deliveryID, Inline: true},
			{Name: "User", Value: fmt.Sprintf("<@%s>", removedDelivery.UserID), Inline: true},
			{Name: "Value", Value: removedDelivery.Value, Inline: true},
			{Name: "Payment Due", Value: valueOrFallback(dueLine(*removedDelivery), "Not set"), Inline: false},
			{Name: "State Entries Cleared", Value: fmt.Sprintf("%d", clearedStateEntries), Inline: true},
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	if removedDelivery.Frequency != "" {
		confirmation.Fields = append(confirmation.Fields, &discordgo.MessageEmbedField{
			Name:   "Frequency",
			Value:  frequencyLabel(removedDelivery.Frequency),
			Inline: true,
		})
	}

	return s.respondEmbeds(interaction.Interaction, "", []*discordgo.MessageEmbed{confirmation})
}

func (s *Service) handleStateClear(interaction *discordgo.InteractionCreate) error {
	options := optionsByName(interaction.ApplicationCommandData().Options)

	deliveryID := requiredString(options, "id")
	if deliveryID == "" {
		return userFacingError{message: "A delivery id is required."}
	}

	reminderID := optionalString(options, "reminder_id")
	dueDate := optionalString(options, "due_date")

	if dueDate != "" && reminderID == "" {
		return userFacingError{message: "due_date can only be used when reminder_id is also provided."}
	}

	cfg, err := s.configStore.Load()
	if err != nil {
		return err
	}

	deliveryConfig, found := findDeliveryByID(cfg.Deliveries, deliveryID)
	if !found {
		return userFacingError{message: fmt.Sprintf("Delivery %q was not found in the config.", deliveryID)}
	}

	if reminderID != "" {
		if _, exists := deliveryConfig.ReminderByID(reminderID); !exists {
			return userFacingError{message: fmt.Sprintf("Reminder %q was not found under delivery %q.", reminderID, deliveryID)}
		}
	}

	if dueDate != "" {
		location, err := time.LoadLocation(cfg.Runtime.Timezone)
		if err != nil {
			return err
		}
		if _, err := time.ParseInLocation("2006-01-02", dueDate, location); err != nil {
			return userFacingError{message: "due_date must use YYYY-MM-DD format."}
		}
	}

	clearedStateEntries, err := s.stateStore.ClearMatching(state.ClearFilter{
		DeliveryID: deliveryID,
		ReminderID: reminderID,
		DueDate:    dueDate,
	})
	if err != nil {
		return err
	}

	color, err := config.ParseHexColor(cfg.Embed.ConfigChangeColor)
	if err != nil {
		return err
	}

	scope := "Entire delivery state"
	if reminderID != "" && dueDate != "" {
		scope = fmt.Sprintf("Reminder `%s` for due date `%s`", reminderID, dueDate)
	} else if reminderID != "" {
		scope = fmt.Sprintf("Reminder `%s` across saved occurrences", reminderID)
	}

	confirmation := &discordgo.MessageEmbed{
		Title:       "State Cleared",
		Description: "Saved send-state was cleared. Matching reminders can send again when you test or when the scheduler reaches them.",
		Color:       color,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "ID", Value: deliveryID, Inline: true},
			{Name: "Scope", Value: scope, Inline: false},
			{Name: "Entries Cleared", Value: fmt.Sprintf("%d", clearedStateEntries), Inline: true},
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	return s.respondEmbeds(interaction.Interaction, "", []*discordgo.MessageEmbed{confirmation})
}

func (s *Service) handleComponent(interaction *discordgo.InteractionCreate) error {
	customID := interaction.MessageComponentData().CustomID
	deliveryID, dueDate, ok := admin.ParseLateReminderCustomID(customID)
	if !ok {
		return userFacingError{message: "Unknown button action."}
	}

	if err := s.deferEphemeralResponse(interaction.Interaction); err != nil {
		return err
	}

	if err := s.handleLateReminder(interaction, deliveryID, dueDate); err != nil {
		return deferredInteractionError{err: err}
	}

	return nil
}

func (s *Service) handleLateReminder(interaction *discordgo.InteractionCreate, deliveryID, dueDate string) error {
	s.logger.Printf("late reminder requested: delivery=%s due=%s channel=%s message=%s", deliveryID, dueDate, interaction.ChannelID, interaction.Message.ID)

	cfg, err := s.configStore.Load()
	if err != nil {
		return err
	}

	if cfg.Discord.AdminChannelID == "" {
		return userFacingError{message: "No admin channel is configured for late reminders."}
	}
	if interaction.ChannelID != cfg.Discord.AdminChannelID {
		return userFacingError{message: "This button can only be used in the configured admin channel."}
	}

	deliveryGroup, ok := findDeliveryByID(cfg.Deliveries, deliveryID)
	if !ok {
		return userFacingError{message: "The delivery for this late reminder could not be found."}
	}

	lateReminder, ok := deliveryGroup.ReminderByID("late")
	if !ok {
		return userFacingError{message: "No late reminder is configured for this delivery."}
	}

	if _, err := time.Parse("2006-01-02", dueDate); err != nil {
		return userFacingError{message: "This late reminder button contains an invalid due date."}
	}

	fileState, err := s.stateStore.Load()
	if err != nil {
		return err
	}

	stateKey := lateReminderStateKey(deliveryID, dueDate)
	if _, exists := fileState.Deliveries[stateKey]; exists {
		if err := s.disableComponentMessage(interaction); err != nil {
			s.logger.Printf("disable late reminder button failed: %v", err)
		}
		return userFacingError{message: "This late reminder has already been sent."}
	}

	location, err := time.LoadLocation(cfg.Runtime.Timezone)
	if err != nil {
		return err
	}

	now := time.Now().In(location)
	lateDelivery := config.ScheduledDelivery{
		StateKey:      stateKey,
		DeliveryID:    deliveryGroup.ID,
		UserID:        deliveryGroup.UserID,
		Value:         deliveryGroup.Value,
		Message:       lateReminder.Message,
		ScheduledAt:   now,
		Date:          now.Format("2006-01-02"),
		Time:          now.Format("15:04"),
		DueDate:       dueDate,
		DueTime:       deliveryGroup.DueTime,
		Frequency:     deliveryGroup.Frequency,
		ReminderID:    "late",
		ReminderName:  lateReminder.Name,
		Title:         lateReminder.Title,
		DaysBeforeDue: 0,
	}

	message := lateDelivery.RenderMessage(cfg.Embed.DescriptionTemplate)
	embed, err := delivery.BuildDeliveryEmbed(s.session, cfg, lateDelivery, message, now)
	if err != nil {
		return err
	}

	if err := delivery.SendEmbedDM(s.session, lateDelivery.UserID, embed); err != nil {
		return userFacingError{message: fmt.Sprintf("I could not DM <@%s> with the late reminder.", lateDelivery.UserID)}
	}

	fileState.Deliveries[stateKey] = state.DeliveryRecord{
		UserID:         lateDelivery.UserID,
		Date:           lateDelivery.Date,
		Time:           lateDelivery.Time,
		DueDate:        lateDelivery.DueDate,
		DueTime:        lateDelivery.DueTime,
		ReminderName:   lateDelivery.ReminderName,
		Value:          lateDelivery.Value,
		Message:        message,
		DeliveredAtUTC: time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.stateStore.Save(fileState); err != nil {
		delete(fileState.Deliveries, stateKey)
		return err
	}

	if err := s.disableComponentMessage(interaction); err != nil {
		s.logger.Printf("disable late reminder button failed: %v", err)
	}

	if err := admin.SendMessage(
		s.session,
		cfg.Discord.AdminChannelID,
		fmt.Sprintf("Late reminder sent to %s | Reminder: %s | Due: %s", lateDelivery.UserMention(), valueOrFallback(lateDelivery.ReminderName, "Late Reminder"), lateDelivery.DueDisplay()),
		embed,
		nil,
	); err != nil {
		s.logger.Printf("send admin late reminder status failed: %v", err)
	}

	s.logger.Printf("late reminder sent: delivery=%s due=%s user=%s", deliveryID, dueDate, lateDelivery.UserID)
	return s.editDeferredText(interaction.Interaction, fmt.Sprintf("Late reminder sent to <@%s>.", lateDelivery.UserID))
}

func applicationCommands() []*discordgo.ApplicationCommand {
	dmPermission := false

	return []*discordgo.ApplicationCommand{
		{
			Name:         commandSendNow,
			Description:  "Send a reminder embed to a user immediately.",
			DMPermission: &dmPermission,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "User to DM.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "value",
					Description: "Value to place into the embed template.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "due_date",
					Description: "Optional due date in YYYY-MM-DD format.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "due_time",
					Description: "Optional due time in HH:MM format.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "message",
					Description: "Optional description override for this send.",
					Required:    false,
				},
			},
		},
		{
			Name:         commandReminderResend,
			Description:  "Manually resend a configured reminder right now.",
			DMPermission: &dmPermission,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "id",
					Description: "Configured delivery id to resend from.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reminder_id",
					Description: "Reminder to send now.",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "initial", Value: "initial"},
						{Name: "final", Value: "final"},
						{Name: "due", Value: "due"},
						{Name: "late", Value: "late"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "due_date",
					Description: "Due date in YYYY-MM-DD format. Required for recurring deliveries.",
					Required:    false,
				},
			},
		},
		{
			Name:         commandScheduleAdd,
			Description:  "Create a payment reminder schedule.",
			DMPermission: &dmPermission,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "User to DM.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "due_date",
					Description: "Payment due date in YYYY-MM-DD format.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "value",
					Description: "Value to place into the embed template.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "initial_time",
					Description: "Initial reminder time in HH:MM format.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "initial_message",
					Description: "Message for the initial reminder.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "final_time",
					Description: "Final reminder time in HH:MM format.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "final_message",
					Description: "Message for the final reminder.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "frequency",
					Description: "How often the due date repeats. Defaults to once.",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "once", Value: "once"},
						{Name: "daily", Value: "daily"},
						{Name: "weekly", Value: "weekly"},
						{Name: "bi-weekly", Value: "bi-weekly"},
						{Name: "monthly", Value: "monthly"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "due_time",
					Description: "Optional payment due time in HH:MM format.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "initial_days_before",
					Description: "Days before due date for initial reminder. Default 3.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "final_days_before",
					Description: "Days before due date for final reminder. Default 1.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "id",
					Description: "Optional stable schedule ID.",
					Required:    false,
				},
			},
		},
		{
			Name:         commandScheduleEdit,
			Description:  "Edit a saved payment schedule and its reminders.",
			DMPermission: &dmPermission,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "id",
					Description: "Configured delivery id to edit.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Optional replacement user to DM.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "due_date",
					Description: "Optional replacement due date in YYYY-MM-DD format.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "value",
					Description: "Optional replacement value.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "frequency",
					Description: "Optional replacement frequency.",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "once", Value: "once"},
						{Name: "daily", Value: "daily"},
						{Name: "weekly", Value: "weekly"},
						{Name: "bi-weekly", Value: "bi-weekly"},
						{Name: "monthly", Value: "monthly"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "due_time",
					Description: "Optional replacement payment due time in HH:MM format.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "initial_title",
					Description: "Optional replacement title for the initial reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "initial_time",
					Description: "Optional replacement time for the initial reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "initial_message",
					Description: "Optional replacement message for the initial reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "initial_days_before",
					Description: "Optional replacement days-before-due for the initial reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "final_title",
					Description: "Optional replacement title for the final reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "final_time",
					Description: "Optional replacement time for the final reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "final_message",
					Description: "Optional replacement message for the final reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "final_days_before",
					Description: "Optional replacement days-before-due for the final reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "due_title",
					Description: "Optional replacement title for the due reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "due_time_reminder",
					Description: "Optional replacement time for the due reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "due_message",
					Description: "Optional replacement message for the due reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "due_days_before",
					Description: "Optional replacement days-before-due for the due reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "late_title",
					Description: "Optional replacement title for the late reminder.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "late_message",
					Description: "Optional replacement message for the late reminder.",
					Required:    false,
				},
			},
		},
		{
			Name:         commandScheduleRemove,
			Description:  "Delete a payment schedule by id and clear its saved state.",
			DMPermission: &dmPermission,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "id",
					Description: "Configured delivery id to remove.",
					Required:    true,
				},
			},
		},
		{
			Name:         commandScheduleListIDs,
			Description:  "List saved delivery ids for quick admin reference.",
			DMPermission: &dmPermission,
		},
		{
			Name:         commandStateClear,
			Description:  "Clear saved send-state for testing or retries.",
			DMPermission: &dmPermission,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "id",
					Description: "Configured delivery id to clear state for.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reminder_id",
					Description: "Optional reminder id to clear, such as initial, final, due, or late.",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "initial", Value: "initial"},
						{Name: "final", Value: "final"},
						{Name: "due", Value: "due"},
						{Name: "late", Value: "late"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "due_date",
					Description: "Optional due date in YYYY-MM-DD format when clearing one recurring occurrence.",
					Required:    false,
				},
			},
		},
		{
			Name:         commandScheduleView,
			Description:  "Show configured schedules from the TOML file.",
			DMPermission: &dmPermission,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "id",
					Description: "Optional delivery id to inspect on its own.",
					Required:    false,
				},
			},
		},
	}
}

func buildScheduleEmbeds(cfg *config.Config, filterID string) ([]*discordgo.MessageEmbed, error) {
	location, err := time.LoadLocation(cfg.Runtime.Timezone)
	if err != nil {
		return nil, err
	}

	color, err := config.ParseHexColor(cfg.Embed.Color)
	if err != nil {
		return nil, err
	}

	visibleGroups := make([]config.Delivery, 0, len(cfg.Deliveries))
	scheduledSendCount := 0
	configuredReminderCount := 0
	for _, deliveryConfig := range cfg.Deliveries {
		if filterID != "" && strings.TrimSpace(deliveryConfig.ID) != strings.TrimSpace(filterID) {
			continue
		}

		scheduledDeliveries, err := deliveryConfig.ExpandAt(location, time.Now().In(location))
		if err != nil {
			return nil, err
		}
		visibleGroups = append(visibleGroups, deliveryConfig)
		scheduledSendCount += len(scheduledDeliveries)
		configuredReminderCount += len(deliveryConfig.Reminders)
	}

	title := "Configured Schedules"
	if filterID != "" {
		title = fmt.Sprintf("Schedule Details: %s", filterID)
	}

	embeds := []*discordgo.MessageEmbed{
		{
			Title: title,
			Description: fmt.Sprintf(
				"Timezone: `%s`\nPoll Interval: `%d seconds`\nGuild Scope: `%s`\nState Path: `%s`\nDelivery Groups: `%d`\nConfigured Reminders: `%d`\nExpanded Occurrences: `%d`",
				cfg.Runtime.Timezone,
				cfg.Runtime.PollIntervalSeconds,
				strings.Join(cfg.Discord.GuildIDs, ", "),
				cfg.Runtime.StatePath,
				len(visibleGroups),
				configuredReminderCount,
				scheduledSendCount,
			),
			Color: color,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "Embed Title",
					Value:  cfg.Embed.Title,
					Inline: false,
				},
				{
					Name:   "Embed Footer",
					Value:  valueOrFallback(cfg.Embed.Footer, "Not set"),
					Inline: false,
				},
				{
					Name:   "Template Preview",
					Value:  trimForField(cfg.Embed.DescriptionTemplate, 1024),
					Inline: false,
				},
			},
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}

	if len(visibleGroups) == 0 {
		embeds[0].Fields = append(embeds[0].Fields, &discordgo.MessageEmbedField{
			Name:   "Schedules",
			Value:  "No matching deliveries are currently configured.",
			Inline: false,
		})
		return embeds, nil
	}

	maxVisibleDeliveries := (maxScheduleEmbeds - 1) * maxFieldsPerEmbed
	displayGroups := visibleGroups
	truncated := false
	if len(displayGroups) > maxVisibleDeliveries {
		displayGroups = displayGroups[:maxVisibleDeliveries]
		truncated = true
	}

	totalPages := (len(displayGroups) + maxFieldsPerEmbed - 1) / maxFieldsPerEmbed
	for page := 0; page < totalPages; page++ {
		start := page * maxFieldsPerEmbed
		end := start + maxFieldsPerEmbed
		if end > len(displayGroups) {
			end = len(displayGroups)
		}

		pageEmbed := &discordgo.MessageEmbed{
			Title:     fmt.Sprintf("Delivery Groups %d/%d", page+1, totalPages),
			Color:     color,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}

		for index, deliveryConfig := range displayGroups[start:end] {
			pageEmbed.Fields = append(pageEmbed.Fields, &discordgo.MessageEmbedField{
				Name:   fmt.Sprintf("%d. %s", start+index+1, trimForField(displayDeliveryLabel(deliveryConfig, start+index), 240)),
				Value:  trimForField(strings.Join(scheduleGroupSummaryLines(deliveryConfig), "\n"), 1024),
				Inline: false,
			})
		}

		embeds = append(embeds, pageEmbed)
	}

	if truncated {
		lastEmbed := embeds[len(embeds)-1]
		lastEmbed.Fields = append(lastEmbed.Fields, &discordgo.MessageEmbedField{
			Name:   "More Schedules",
			Value:  fmt.Sprintf("Only the first %d delivery groups are shown in this response.", maxVisibleDeliveries),
			Inline: false,
		})
	}

	return embeds, nil
}

func buildScheduleIDEmbeds(cfg *config.Config) ([]*discordgo.MessageEmbed, error) {
	color, err := config.ParseHexColor(cfg.Embed.Color)
	if err != nil {
		return nil, err
	}

	embeds := []*discordgo.MessageEmbed{
		{
			Title:       "Configured Delivery IDs",
			Description: fmt.Sprintf("Total delivery groups: `%d`", len(cfg.Deliveries)),
			Color:       color,
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		},
	}

	if len(cfg.Deliveries) == 0 {
		embeds[0].Fields = append(embeds[0].Fields, &discordgo.MessageEmbedField{
			Name:   "Schedules",
			Value:  "No delivery groups are currently configured.",
			Inline: false,
		})
		return embeds, nil
	}

	maxVisibleDeliveries := (maxScheduleEmbeds - 1) * maxFieldsPerEmbed
	displayGroups := cfg.Deliveries
	truncated := false
	if len(displayGroups) > maxVisibleDeliveries {
		displayGroups = displayGroups[:maxVisibleDeliveries]
		truncated = true
	}

	totalPages := (len(displayGroups) + maxFieldsPerEmbed - 1) / maxFieldsPerEmbed
	for page := 0; page < totalPages; page++ {
		start := page * maxFieldsPerEmbed
		end := start + maxFieldsPerEmbed
		if end > len(displayGroups) {
			end = len(displayGroups)
		}

		pageEmbed := &discordgo.MessageEmbed{
			Title:     fmt.Sprintf("Schedule IDs %d/%d", page+1, totalPages),
			Color:     color,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}

		for index, deliveryConfig := range displayGroups[start:end] {
			pageEmbed.Fields = append(pageEmbed.Fields, &discordgo.MessageEmbedField{
				Name:   fmt.Sprintf("%d. %s", start+index+1, trimForField(displayDeliveryLabel(deliveryConfig, start+index), 240)),
				Value:  trimForField(strings.Join(scheduleGroupSummaryLines(deliveryConfig), "\n"), 1024),
				Inline: false,
			})
		}

		embeds = append(embeds, pageEmbed)
	}

	if truncated {
		lastEmbed := embeds[len(embeds)-1]
		lastEmbed.Fields = append(lastEmbed.Fields, &discordgo.MessageEmbedField{
			Name:   "More Schedules",
			Value:  fmt.Sprintf("Only the first %d delivery groups are shown in this response.", maxVisibleDeliveries),
			Inline: false,
		})
	}

	return embeds, nil
}

func optionsByName(options []*discordgo.ApplicationCommandInteractionDataOption) map[string]*discordgo.ApplicationCommandInteractionDataOption {
	optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
	for _, option := range options {
		optionMap[option.Name] = option
	}

	return optionMap
}

func optionalString(options map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	option, exists := options[name]
	if !exists {
		return ""
	}

	return strings.TrimSpace(option.StringValue())
}

func requiredString(options map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	return strings.TrimSpace(options[name].StringValue())
}

func optionalInt(options map[string]*discordgo.ApplicationCommandInteractionDataOption, name string, fallback int) int {
	option, exists := options[name]
	if !exists {
		return fallback
	}

	return int(option.IntValue())
}

func optionalOptionalInt(options map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) *int {
	option, exists := options[name]
	if !exists {
		return nil
	}

	value := int(option.IntValue())
	return &value
}

type reminderEdit struct {
	Title         string
	Time          string
	Message       string
	DaysBeforeDue *int
	DefaultName   string
}

func applyReminderEdit(deliveryConfig *config.Delivery, reminderID string, edit reminderEdit) {
	if deliveryConfig == nil || !edit.hasValues() {
		return
	}

	index := -1
	for i := range deliveryConfig.Reminders {
		if strings.EqualFold(strings.TrimSpace(deliveryConfig.Reminders[i].ID), reminderID) {
			index = i
			break
		}
	}

	if index == -1 {
		deliveryConfig.Reminders = append(deliveryConfig.Reminders, config.Reminder{
			ID:   reminderID,
			Name: edit.DefaultName,
		})
		index = len(deliveryConfig.Reminders) - 1
	}

	reminder := &deliveryConfig.Reminders[index]
	if reminder.Name == "" {
		reminder.Name = edit.DefaultName
	}
	if edit.Title != "" {
		reminder.Title = edit.Title
	}
	if edit.Time != "" {
		reminder.Time = edit.Time
	}
	if edit.Message != "" {
		reminder.Message = edit.Message
	}
	if edit.DaysBeforeDue != nil {
		reminder.DaysBeforeDue = *edit.DaysBeforeDue
	}
}

func (r reminderEdit) hasValues() bool {
	return strings.TrimSpace(r.Title) != "" ||
		strings.TrimSpace(r.Time) != "" ||
		strings.TrimSpace(r.Message) != "" ||
		r.DaysBeforeDue != nil
}

func (s *Service) respondError(interaction *discordgo.Interaction, message string) error {
	return s.session.InteractionRespond(interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: message,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func (s *Service) respondEmbeds(interaction *discordgo.Interaction, content string, embeds []*discordgo.MessageEmbed) error {
	return s.session.InteractionRespond(interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Embeds:  embeds,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func (s *Service) respondText(interaction *discordgo.Interaction, content string) error {
	return s.session.InteractionRespond(interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func (s *Service) deferEphemeralResponse(interaction *discordgo.Interaction) error {
	return s.session.InteractionRespond(interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
}

func (s *Service) editDeferredText(interaction *discordgo.Interaction, content string) error {
	_, err := s.session.InteractionResponseEdit(interaction, &discordgo.WebhookEdit{
		Content: &content,
	})
	return err
}

func boolLabel(value bool) string {
	if value {
		return "Yes"
	}

	return "No"
}

func trimForField(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}

	return value[:maxLength-3] + "..."
}

func valueOrFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}

	return value
}

func buildAdminManualResendContent(deliveryConfig config.ScheduledDelivery, reminderID string) string {
	lines := []string{
		"**Reminder Resent Manually**",
		fmt.Sprintf("User: %s", deliveryConfig.UserMention()),
		fmt.Sprintf("Reminder: %s", valueOrFallback(deliveryConfig.ReminderName, reminderID)),
		fmt.Sprintf("Due: %s", deliveryConfig.DueDisplay()),
	}

	return strings.Join(lines, "\n")
}

func displayDeliveryLabel(deliveryConfig config.Delivery, index int) string {
	if strings.TrimSpace(deliveryConfig.ID) != "" {
		return deliveryConfig.ID
	}
	if strings.TrimSpace(deliveryConfig.DueDate) != "" {
		return fmt.Sprintf("(no id) user:%s due:%s #%d", deliveryConfig.UserID, deliveryConfig.DueDate, index+1)
	}
	return fmt.Sprintf("(no id) user:%s at:%s %s #%d", deliveryConfig.UserID, deliveryConfig.Date, deliveryConfig.Time, index+1)
}

func scheduleGroupSummaryLines(deliveryConfig config.Delivery) []string {
	lines := []string{
		fmt.Sprintf("User: <@%s>", deliveryConfig.UserID),
		fmt.Sprintf("Value: `%s`", deliveryConfig.Value),
	}

	if deliveryConfig.DueDate != "" {
		lines = append(lines,
			fmt.Sprintf("Due: %s", dueLine(deliveryConfig)),
			fmt.Sprintf("Frequency: %s", frequencyLabel(deliveryConfig.Frequency)),
		)
	} else {
		lines = append(lines,
			fmt.Sprintf("When: %s %s", deliveryConfig.Date, deliveryConfig.Time),
			fmt.Sprintf("Custom Text: %s", boolLabel(deliveryConfig.Message != "")),
		)
	}

	if len(deliveryConfig.Reminders) > 0 {
		reminderLines := make([]string, 0, len(deliveryConfig.Reminders))
		for _, reminder := range deliveryConfig.Reminders {
			line := valueOrFallback(reminder.ID, reminder.Name)
			if reminder.ManualOnly() {
				line += " (manual)"
			} else {
				line += fmt.Sprintf(" - %d days before at %s", reminder.DaysBeforeDue, reminder.Time)
			}
			reminderLines = append(reminderLines, line)
		}
		lines = append(lines, "Reminders:\n"+strings.Join(reminderLines, "\n"))
	}

	return lines
}

func dueLine(deliveryConfig config.Delivery) string {
	if deliveryConfig.DueTime != "" {
		return deliveryConfig.DueDate + " " + deliveryConfig.DueTime
	}

	return deliveryConfig.DueDate
}

func frequencyLabel(value string) string {
	if strings.TrimSpace(value) == "" {
		return "once"
	}

	return strings.ToLower(strings.TrimSpace(value))
}

func findDeliveryByID(deliveries []config.Delivery, deliveryID string) (config.Delivery, bool) {
	for _, deliveryConfig := range deliveries {
		if strings.TrimSpace(deliveryConfig.ID) == strings.TrimSpace(deliveryID) {
			return deliveryConfig, true
		}
	}

	return config.Delivery{}, false
}

func lateReminderStateKey(deliveryID, dueDate string) string {
	return "late:" + strings.TrimSpace(deliveryID) + ":" + strings.TrimSpace(dueDate)
}

func adminMessageBody(deliveryConfig config.ScheduledDelivery, extraLines ...string) string {
	lines := []string{
		fmt.Sprintf("User: %s", deliveryConfig.UserMention()),
		fmt.Sprintf("Value: **%s**", deliveryConfig.Value),
		fmt.Sprintf("Reminder: **%s**", valueOrFallback(deliveryConfig.ReminderName, "Late Reminder")),
		fmt.Sprintf("Due: **%s**", deliveryConfig.DueDisplay()),
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

func (s *Service) disableComponentMessage(interaction *discordgo.InteractionCreate) error {
	if interaction.Message == nil {
		return nil
	}

	disabledComponents := admin.DisableComponents(interaction.Message.Components)
	_, err := s.session.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID:         interaction.Message.ID,
		Channel:    interaction.ChannelID,
		Content:    &interaction.Message.Content,
		Embeds:     &interaction.Message.Embeds,
		Components: &disabledComponents,
	})
	return err
}

func (s *Service) memberHasAllowedRole(member *discordgo.Member) bool {
	if member == nil {
		return false
	}

	for _, roleID := range member.Roles {
		if _, allowed := s.allowedRoleIDs[roleID]; allowed {
			return true
		}
	}

	return false
}

func (s *Service) guildAllowed(guildID string) bool {
	_, allowed := s.guildIDs[guildID]
	return allowed
}
