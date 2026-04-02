package delivery

import (
	"fmt"
	"time"

	"discorddmbot/internal/config"

	"github.com/bwmarrin/discordgo"
)

func BuildDeliveryEmbed(session *discordgo.Session, cfg *config.Config, deliveryConfig config.ScheduledDelivery, message string, scheduledAt time.Time) (*discordgo.MessageEmbed, error) {
	color, err := config.ParseHexColor(cfg.Embed.Color)
	if err != nil {
		return nil, fmt.Errorf("parse embed color: %w", err)
	}

	avatarURL := botAvatarURL(session)
	dueValue := scheduledAt.Format("2006-01-02 15:04 MST")
	if deliveryConfig.DueDate != "" {
		dueValue = deliveryConfig.DueDate
		if deliveryConfig.DueTime != "" {
			dueValue += " " + deliveryConfig.DueTime
		}
	}

	fields := []*discordgo.MessageEmbedField{
		{
			Name:   "Value",
			Value:  deliveryConfig.Value,
			Inline: true,
		},
		{
			Name:   "Due",
			Value:  dueValue,
			Inline: true,
		},
	}

	if deliveryConfig.ReminderName != "" {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Reminder",
			Value:  deliveryConfig.ReminderName,
			Inline: true,
		})
	}

	embed := &discordgo.MessageEmbed{
		Title:       cfg.Embed.Title,
		Description: message,
		Color:       color,
		Footer: &discordgo.MessageEmbedFooter{
			Text:    cfg.Embed.Footer,
			IconURL: avatarURL,
		},
		Fields:    fields,
		Thumbnail: &discordgo.MessageEmbedThumbnail{URL: avatarURL},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	if cfg.Embed.Footer == "" {
		embed.Footer = nil
	}
	if avatarURL == "" {
		embed.Thumbnail = nil
	}

	return embed, nil
}

func botAvatarURL(session *discordgo.Session) string {
	if session == nil || session.State == nil || session.State.User == nil {
		return ""
	}

	return session.State.User.AvatarURL("256")
}

func SendEmbedDM(session *discordgo.Session, userID string, embed *discordgo.MessageEmbed) error {
	channel, err := session.UserChannelCreate(userID)
	if err != nil {
		return fmt.Errorf("create dm channel: %w", err)
	}

	if _, err := session.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{Embed: embed}); err != nil {
		return fmt.Errorf("send embed dm: %w", err)
	}

	return nil
}

func EnsureUserInGuild(session *discordgo.Session, guildID, userID string) error {
	if _, err := session.GuildMember(guildID, userID); err != nil {
		return fmt.Errorf("ensure guild member: %w", err)
	}

	return nil
}

func EnsureUserInAnyGuild(session *discordgo.Session, guildIDs []string, userID string) (string, error) {
	var lastErr error
	for _, guildID := range guildIDs {
		if err := EnsureUserInGuild(session, guildID, userID); err == nil {
			return guildID, nil
		} else {
			lastErr = err
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no configured guilds available")
	}

	return "", lastErr
}
