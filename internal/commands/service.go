package commands

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"discorddmbot/internal/config"
	"discorddmbot/internal/delivery"

	"github.com/bwmarrin/discordgo"
)

const (
	commandSendNow      = "send-now"
	commandScheduleAdd  = "schedule-add"
	commandScheduleView = "schedule-view"
	maxScheduleEmbeds   = 10
	maxFieldsPerEmbed   = 5
)

type Service struct {
	session        *discordgo.Session
	configStore    *config.Store
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

func NewService(session *discordgo.Session, configStore *config.Store, logger *log.Logger, discordConfig config.Discord) *Service {
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
	if interaction.Type != discordgo.InteractionApplicationCommand {
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
	switch interaction.ApplicationCommandData().Name {
	case commandSendNow:
		err = s.handleSendNow(interaction)
	case commandScheduleAdd:
		err = s.handleScheduleAdd(interaction)
	case commandScheduleView:
		err = s.handleScheduleView(interaction)
	default:
		err = s.respondError(interaction.Interaction, "Unknown command.")
	}

	if err != nil {
		s.logger.Printf("slash command %s failed: %v", interaction.ApplicationCommandData().Name, err)
		var userErr userFacingError
		responseMessage := "The command could not be completed. Check the bot logs for details."
		if errors.As(err, &userErr) {
			responseMessage = userErr.message
		}
		if responseErr := s.respondError(interaction.Interaction, responseMessage); responseErr != nil {
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
	deliveryConfig := config.ScheduledDelivery{
		UserID:      user.ID,
		Date:        now.Format("2006-01-02"),
		Time:        now.Format("15:04"),
		Value:       strings.TrimSpace(options["value"].StringValue()),
		Message:     optionalString(options, "message"),
		ScheduledAt: now,
	}

	message := deliveryConfig.RenderMessage(cfg.Embed.DescriptionTemplate)
	embed, err := delivery.BuildDeliveryEmbed(cfg, deliveryConfig, message, now)
	if err != nil {
		return err
	}

	if err := delivery.SendEmbedDM(s.session, deliveryConfig.UserID, embed); err != nil {
		return userFacingError{message: fmt.Sprintf("I could not DM <@%s>. They may have DMs disabled or not share a server with the bot.", deliveryConfig.UserID)}
	}

	return s.respondEmbeds(interaction.Interaction, fmt.Sprintf("Sent the embed to <@%s>.", deliveryConfig.UserID), []*discordgo.MessageEmbed{embed})
}

func (s *Service) handleScheduleAdd(interaction *discordgo.InteractionCreate) error {
	options := optionsByName(interaction.ApplicationCommandData().Options)

	user := options["user"].UserValue(nil)
	if user == nil {
		return userFacingError{message: "A valid Discord user is required."}
	}

	newDelivery := config.Delivery{
		ID:      optionalString(options, "id"),
		UserID:  user.ID,
		DueDate: strings.TrimSpace(options["due_date"].StringValue()),
		DueTime: optionalString(options, "due_time"),
		Value:   strings.TrimSpace(options["value"].StringValue()),
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

	expandedDeliveries, err := newDelivery.Expand(location)
	if err != nil {
		return err
	}

	color, err := config.ParseHexColor(cfg.Embed.Color)
	if err != nil {
		return err
	}

	confirmation := &discordgo.MessageEmbed{
		Title:       "Schedule Saved",
		Description: "The payment reminder schedule was written to the config file and will be picked up by the scheduler automatically.",
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
	cfg, err := s.configStore.Load()
	if err != nil {
		return err
	}

	embeds, err := buildScheduleEmbeds(cfg)
	if err != nil {
		return err
	}

	return s.respondEmbeds(interaction.Interaction, "", embeds)
}

func applicationCommands() []*discordgo.ApplicationCommand {
	dmPermission := false

	return []*discordgo.ApplicationCommand{
		{
			Name:         commandSendNow,
			Description:  "Send the configured embed to a user immediately.",
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
					Description: "Value to inject into the embed template.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "message",
					Description: "Optional custom embed description override.",
					Required:    false,
				},
			},
		},
		{
			Name:         commandScheduleAdd,
			Description:  "Add a payment schedule with initial and final reminders.",
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
					Name:        "due_time",
					Description: "Optional payment due time in HH:MM format.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "value",
					Description: "Value to inject into the embed template.",
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
					Description: "Optional stable payment schedule ID.",
					Required:    false,
				},
			},
		},
		{
			Name:         commandScheduleView,
			Description:  "Read the current config as parsed embed pages.",
			DMPermission: &dmPermission,
		},
	}
}

func buildScheduleEmbeds(cfg *config.Config) ([]*discordgo.MessageEmbed, error) {
	location, err := time.LoadLocation(cfg.Runtime.Timezone)
	if err != nil {
		return nil, err
	}

	color, err := config.ParseHexColor(cfg.Embed.Color)
	if err != nil {
		return nil, err
	}

	expandedDeliveries := make([]config.ScheduledDelivery, 0)
	for _, deliveryConfig := range cfg.Deliveries {
		scheduledDeliveries, err := deliveryConfig.Expand(location)
		if err != nil {
			return nil, err
		}
		expandedDeliveries = append(expandedDeliveries, scheduledDeliveries...)
	}

	embeds := []*discordgo.MessageEmbed{
		{
			Title: "Configured Schedule",
			Description: fmt.Sprintf(
				"Timezone: `%s`\nPoll Interval: `%d seconds`\nGuild Scope: `%s`\nState Path: `%s`\nDelivery Groups: `%d`\nScheduled Sends: `%d`",
				cfg.Runtime.Timezone,
				cfg.Runtime.PollIntervalSeconds,
				strings.Join(cfg.Discord.GuildIDs, ", "),
				cfg.Runtime.StatePath,
				len(cfg.Deliveries),
				len(expandedDeliveries),
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

	if len(cfg.Deliveries) == 0 {
		embeds[0].Fields = append(embeds[0].Fields, &discordgo.MessageEmbedField{
			Name:   "Deliveries",
			Value:  "No scheduled deliveries are currently configured.",
			Inline: false,
		})
		return embeds, nil
	}

	maxVisibleDeliveries := (maxScheduleEmbeds - 1) * maxFieldsPerEmbed
	visibleDeliveries := expandedDeliveries
	truncated := false
	if len(visibleDeliveries) > maxVisibleDeliveries {
		visibleDeliveries = visibleDeliveries[:maxVisibleDeliveries]
		truncated = true
	}

	totalPages := (len(visibleDeliveries) + maxFieldsPerEmbed - 1) / maxFieldsPerEmbed
	for page := 0; page < totalPages; page++ {
		start := page * maxFieldsPerEmbed
		end := start + maxFieldsPerEmbed
		if end > len(visibleDeliveries) {
			end = len(visibleDeliveries)
		}

		pageEmbed := &discordgo.MessageEmbed{
			Title:     fmt.Sprintf("Scheduled Deliveries %d/%d", page+1, totalPages),
			Color:     color,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}

		for index, deliveryConfig := range visibleDeliveries[start:end] {
			label := deliveryConfig.DeliveryID
			if label == "" {
				label = deliveryConfig.StateKey
			}
			if deliveryConfig.ReminderName != "" {
				label += " / " + deliveryConfig.ReminderName
			}

			fieldLines := []string{
				fmt.Sprintf("User: <@%s>", deliveryConfig.UserID),
				fmt.Sprintf("When: %s", deliveryConfig.ScheduledAt.Format("2006-01-02 15:04 MST")),
				fmt.Sprintf("Value: `%s`", deliveryConfig.Value),
				fmt.Sprintf("Custom Description: %s", boolLabel(deliveryConfig.Message != "")),
			}
			if deliveryConfig.ReminderName != "" {
				fieldLines = append(fieldLines,
					fmt.Sprintf("Reminder: %s", deliveryConfig.ReminderName),
					fmt.Sprintf("Days Before Due: %d", deliveryConfig.DaysBeforeDue),
				)
			}
			if deliveryConfig.DueDate != "" {
				dueLine := deliveryConfig.DueDate
				if deliveryConfig.DueTime != "" {
					dueLine += " " + deliveryConfig.DueTime
				}
				fieldLines = append(fieldLines, fmt.Sprintf("Payment Due: %s", dueLine))
			}

			fieldValue := strings.Join(fieldLines, "\n")

			pageEmbed.Fields = append(pageEmbed.Fields, &discordgo.MessageEmbedField{
				Name:   fmt.Sprintf("%d. %s", start+index+1, trimForField(label, 240)),
				Value:  trimForField(fieldValue, 1024),
				Inline: false,
			})
		}

		embeds = append(embeds, pageEmbed)
	}

	if truncated {
		lastEmbed := embeds[len(embeds)-1]
		lastEmbed.Fields = append(lastEmbed.Fields, &discordgo.MessageEmbedField{
			Name:   "More Deliveries",
			Value:  fmt.Sprintf("Only the first %d deliveries are shown in this response.", maxVisibleDeliveries),
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

func dueLine(deliveryConfig config.Delivery) string {
	if deliveryConfig.DueTime != "" {
		return deliveryConfig.DueDate + " " + deliveryConfig.DueTime
	}

	return deliveryConfig.DueDate
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
