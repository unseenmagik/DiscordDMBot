package admin

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const lateReminderPrefix = "late-reminder"

func SendMessage(session *discordgo.Session, channelID string, embed *discordgo.MessageEmbed, components []discordgo.MessageComponent) error {
	if channelID == "" {
		return nil
	}

	message := &discordgo.MessageSend{
		Embed:      embed,
		Components: components,
	}

	if _, err := session.ChannelMessageSendComplex(channelID, message); err != nil {
		return fmt.Errorf("send admin channel message: %w", err)
	}

	return nil
}

func DisableComponents(components []discordgo.MessageComponent) []discordgo.MessageComponent {
	disabled := make([]discordgo.MessageComponent, 0, len(components))
	for _, component := range components {
		row, ok := component.(discordgo.ActionsRow)
		if !ok {
			continue
		}

		newRow := discordgo.ActionsRow{Components: make([]discordgo.MessageComponent, 0, len(row.Components))}
		for _, child := range row.Components {
			button, ok := child.(discordgo.Button)
			if !ok {
				continue
			}
			button.Disabled = true
			newRow.Components = append(newRow.Components, button)
		}

		disabled = append(disabled, newRow)
	}

	return disabled
}

func LateReminderComponents(deliveryID, dueDate string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Late reminder",
					Style:    discordgo.PrimaryButton,
					CustomID: BuildLateReminderCustomID(deliveryID, dueDate),
				},
			},
		},
	}
}

func BuildLateReminderCustomID(deliveryID, dueDate string) string {
	encodedID := base64.RawURLEncoding.EncodeToString([]byte(deliveryID))
	return lateReminderPrefix + ":" + encodedID + ":" + dueDate
}

func ParseLateReminderCustomID(customID string) (string, string, bool) {
	parts := strings.Split(customID, ":")
	if len(parts) != 3 || parts[0] != lateReminderPrefix {
		return "", "", false
	}

	decodedID, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}

	return string(decodedID), parts[2], true
}

func StatusEmbed(title, description string, color int) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       title,
		Description: description,
		Color:       color,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
}
