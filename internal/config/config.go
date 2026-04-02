package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

var discordUserIDPattern = regexp.MustCompile(`^\d{10,20}$`)

type Config struct {
	Discord       Discord       `toml:"discord"`
	Runtime       Runtime       `toml:"runtime"`
	Embed         Embed         `toml:"embed"`
	Notifications Notifications `toml:"notifications"`
	Deliveries    []Delivery    `toml:"deliveries"`
}

type Discord struct {
	BotToken       string   `toml:"bot_token"`
	GuildIDs       []string `toml:"guild_ids"`
	AllowedRoleIDs []string `toml:"allowed_role_ids"`
}

type Runtime struct {
	Timezone             string `toml:"timezone"`
	PollIntervalSeconds  int    `toml:"poll_interval_seconds"`
	SendMissedDeliveries bool   `toml:"send_missed_deliveries"`
	StatePath            string `toml:"state_path"`
}

type Embed struct {
	Title               string `toml:"title"`
	DescriptionTemplate string `toml:"description_template"`
	Footer              string `toml:"footer"`
	Color               string `toml:"color"`
}

type Notifications struct {
	DiscordWebhookURL string `toml:"discord_webhook_url"`
	NotifySent        bool   `toml:"notify_sent"`
	NotifySkipped     bool   `toml:"notify_skipped"`
	NotifyFailed      bool   `toml:"notify_failed"`
}

type Delivery struct {
	ID        string     `toml:"id,omitempty"`
	UserID    string     `toml:"user_id"`
	Date      string     `toml:"date"`
	Time      string     `toml:"time"`
	Message   string     `toml:"message,omitempty"`
	Value     string     `toml:"value"`
	DueDate   string     `toml:"due_date"`
	DueTime   string     `toml:"due_time"`
	Reminders []Reminder `toml:"reminders"`
}

type Reminder struct {
	ID            string `toml:"id,omitempty"`
	Name          string `toml:"name"`
	DaysBeforeDue int    `toml:"days_before_due"`
	Time          string `toml:"time"`
	Message       string `toml:"message"`
}

type ScheduledDelivery struct {
	StateKey      string
	DeliveryID    string
	UserID        string
	Value         string
	Message       string
	ScheduledAt   time.Time
	Date          string
	Time          string
	DueDate       string
	DueTime       string
	ReminderID    string
	ReminderName  string
	DaysBeforeDue int
}

func Load(path string) (*Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("read config file: %w; create %s from config/config.toml.example", err, path)
		}
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	metadata, err := toml.Decode(string(content), &cfg)
	if err != nil {
		return nil, fmt.Errorf("decode config file: %w", err)
	}

	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}

		return nil, fmt.Errorf("unknown config fields: %s", strings.Join(keys, ", "))
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c *Config) Validate() error {
	c.Discord.BotToken = strings.TrimSpace(c.Discord.BotToken)
	for index := range c.Discord.GuildIDs {
		c.Discord.GuildIDs[index] = strings.TrimSpace(c.Discord.GuildIDs[index])
	}
	for index := range c.Discord.AllowedRoleIDs {
		c.Discord.AllowedRoleIDs[index] = strings.TrimSpace(c.Discord.AllowedRoleIDs[index])
	}
	c.Runtime.Timezone = strings.TrimSpace(c.Runtime.Timezone)
	c.Runtime.StatePath = strings.TrimSpace(c.Runtime.StatePath)
	c.Embed.Title = strings.TrimSpace(c.Embed.Title)
	c.Embed.DescriptionTemplate = strings.TrimSpace(c.Embed.DescriptionTemplate)
	c.Embed.Footer = strings.TrimSpace(c.Embed.Footer)
	c.Embed.Color = strings.TrimSpace(c.Embed.Color)
	c.Notifications.DiscordWebhookURL = strings.TrimSpace(c.Notifications.DiscordWebhookURL)

	if c.Runtime.PollIntervalSeconds <= 0 {
		c.Runtime.PollIntervalSeconds = 15
	}

	if c.Runtime.StatePath == "" {
		c.Runtime.StatePath = "data/delivery-state.json"
	}

	if c.Discord.BotToken == "" {
		return fmt.Errorf("discord.bot_token is required")
	}

	if c.Runtime.Timezone == "" {
		return fmt.Errorf("runtime.timezone is required")
	}

	location, err := time.LoadLocation(c.Runtime.Timezone)
	if err != nil {
		return fmt.Errorf("invalid runtime.timezone %q: %w", c.Runtime.Timezone, err)
	}

	if len(c.Discord.GuildIDs) == 0 {
		return fmt.Errorf("discord.guild_ids must include at least one Discord guild ID")
	}

	seenGuildIDs := make(map[string]struct{}, len(c.Discord.GuildIDs))
	for index, guildID := range c.Discord.GuildIDs {
		if guildID == "" {
			return fmt.Errorf("discord.guild_ids[%d] is required", index)
		}
		if !discordUserIDPattern.MatchString(guildID) {
			return fmt.Errorf("discord.guild_ids[%d] must be a Discord snowflake", index)
		}
		if _, exists := seenGuildIDs[guildID]; exists {
			return fmt.Errorf("duplicate discord.guild_ids entry %q detected", guildID)
		}
		seenGuildIDs[guildID] = struct{}{}
	}

	if len(c.Discord.AllowedRoleIDs) == 0 {
		return fmt.Errorf("discord.allowed_role_ids must include at least one Discord role ID")
	}

	for index, roleID := range c.Discord.AllowedRoleIDs {
		if roleID == "" {
			return fmt.Errorf("discord.allowed_role_ids[%d] is required", index)
		}
		if !discordUserIDPattern.MatchString(roleID) {
			return fmt.Errorf("discord.allowed_role_ids[%d] must be a Discord snowflake", index)
		}
	}

	if c.Embed.Title == "" {
		return fmt.Errorf("embed.title is required")
	}

	if c.Embed.DescriptionTemplate == "" {
		return fmt.Errorf("embed.description_template is required")
	}

	if c.Embed.Color == "" {
		c.Embed.Color = "#2B6CB0"
	}

	if _, err := ParseHexColor(c.Embed.Color); err != nil {
		return fmt.Errorf("invalid embed.color: %w", err)
	}

	if c.Notifications.DiscordWebhookURL != "" {
		parsedURL, err := url.Parse(c.Notifications.DiscordWebhookURL)
		if err != nil {
			return fmt.Errorf("invalid notifications.discord_webhook_url: %w", err)
		}
		if parsedURL.Scheme != "https" || parsedURL.Host == "" {
			return fmt.Errorf("notifications.discord_webhook_url must be a valid https URL")
		}
	} else if c.Notifications.NotifySent || c.Notifications.NotifySkipped || c.Notifications.NotifyFailed {
		return fmt.Errorf("notifications.discord_webhook_url is required when notification events are enabled")
	}

	seen := make(map[string]struct{}, len(c.Deliveries))
	for index := range c.Deliveries {
		delivery := &c.Deliveries[index]
		delivery.ID = strings.TrimSpace(delivery.ID)
		delivery.UserID = strings.TrimSpace(delivery.UserID)
		delivery.Date = strings.TrimSpace(delivery.Date)
		delivery.Time = strings.TrimSpace(delivery.Time)
		delivery.Message = strings.TrimSpace(delivery.Message)
		delivery.Value = strings.TrimSpace(delivery.Value)
		delivery.DueDate = strings.TrimSpace(delivery.DueDate)
		delivery.DueTime = strings.TrimSpace(delivery.DueTime)
		for reminderIndex := range delivery.Reminders {
			delivery.Reminders[reminderIndex].ID = strings.TrimSpace(delivery.Reminders[reminderIndex].ID)
			delivery.Reminders[reminderIndex].Name = strings.TrimSpace(delivery.Reminders[reminderIndex].Name)
			delivery.Reminders[reminderIndex].Time = strings.TrimSpace(delivery.Reminders[reminderIndex].Time)
			delivery.Reminders[reminderIndex].Message = strings.TrimSpace(delivery.Reminders[reminderIndex].Message)
		}

		if delivery.UserID == "" {
			return fmt.Errorf("deliveries[%d].user_id is required", index)
		}
		if !discordUserIDPattern.MatchString(delivery.UserID) {
			return fmt.Errorf("deliveries[%d].user_id must be a Discord snowflake", index)
		}
		if delivery.Value == "" {
			return fmt.Errorf("deliveries[%d].value is required", index)
		}

		isLegacySchedule := delivery.Date != "" || delivery.Time != "" || delivery.Message != ""
		isReminderSchedule := delivery.DueDate != "" || delivery.DueTime != "" || len(delivery.Reminders) > 0

		if isLegacySchedule && isReminderSchedule {
			return fmt.Errorf("deliveries[%d] must use either direct date/time scheduling or due_date/reminders, not both", index)
		}

		if !isLegacySchedule && !isReminderSchedule {
			return fmt.Errorf("deliveries[%d] must define either date/time or due_date with reminders", index)
		}

		if isLegacySchedule {
			if delivery.Date == "" {
				return fmt.Errorf("deliveries[%d].date is required", index)
			}
			if delivery.Time == "" {
				return fmt.Errorf("deliveries[%d].time is required", index)
			}
			if delivery.Message == "" && c.Embed.DescriptionTemplate == "" {
				return fmt.Errorf("deliveries[%d] requires either message or embed.description_template", index)
			}

			if _, err := delivery.ScheduledAt(location); err != nil {
				return fmt.Errorf("deliveries[%d] has invalid date/time: %w", index, err)
			}
		}

		if isReminderSchedule {
			if delivery.DueDate == "" {
				return fmt.Errorf("deliveries[%d].due_date is required", index)
			}
			if delivery.DueTime != "" {
				if _, err := time.ParseInLocation("15:04", delivery.DueTime, location); err != nil {
					return fmt.Errorf("deliveries[%d].due_time is invalid: %w", index, err)
				}
			}
			if _, err := time.ParseInLocation("2006-01-02", delivery.DueDate, location); err != nil {
				return fmt.Errorf("deliveries[%d].due_date is invalid: %w", index, err)
			}
			if len(delivery.Reminders) == 0 {
				return fmt.Errorf("deliveries[%d].reminders must include at least one reminder", index)
			}

			seenReminderKeys := make(map[string]struct{}, len(delivery.Reminders))
			for reminderIndex := range delivery.Reminders {
				reminder := delivery.Reminders[reminderIndex]
				if reminder.Name == "" {
					return fmt.Errorf("deliveries[%d].reminders[%d].name is required", index, reminderIndex)
				}
				if reminder.Time == "" {
					return fmt.Errorf("deliveries[%d].reminders[%d].time is required", index, reminderIndex)
				}
				if reminder.Message == "" && c.Embed.DescriptionTemplate == "" {
					return fmt.Errorf("deliveries[%d].reminders[%d].message is required", index, reminderIndex)
				}
				if reminder.DaysBeforeDue < 0 {
					return fmt.Errorf("deliveries[%d].reminders[%d].days_before_due must be zero or greater", index, reminderIndex)
				}
				if _, err := time.ParseInLocation("15:04", reminder.Time, location); err != nil {
					return fmt.Errorf("deliveries[%d].reminders[%d].time is invalid: %w", index, reminderIndex, err)
				}

				reminderKey := reminder.keyPart()
				if _, exists := seenReminderKeys[reminderKey]; exists {
					return fmt.Errorf("deliveries[%d] has duplicate reminder key %q", index, reminderKey)
				}
				seenReminderKeys[reminderKey] = struct{}{}
			}
		}

		scheduledDeliveries, err := delivery.Expand(location)
		if err != nil {
			return fmt.Errorf("deliveries[%d] expansion failed: %w", index, err)
		}

		for _, scheduledDelivery := range scheduledDeliveries {
			if _, exists := seen[scheduledDelivery.StateKey]; exists {
				return fmt.Errorf("duplicate delivery id/state key %q detected", scheduledDelivery.StateKey)
			}
			seen[scheduledDelivery.StateKey] = struct{}{}
		}
	}

	return nil
}

func (d Delivery) ScheduledAt(location *time.Location) (time.Time, error) {
	scheduledAt, err := time.ParseInLocation("2006-01-02 15:04", d.Date+" "+d.Time, location)
	if err != nil {
		return time.Time{}, err
	}

	return scheduledAt, nil
}

func (d Delivery) StateKey() string {
	if d.ID != "" {
		return "custom:" + d.ID
	}

	sum := sha256.Sum256([]byte(strings.Join([]string{d.UserID, d.Date, d.Time}, "|")))
	return "auto:" + hex.EncodeToString(sum[:])
}

func (d Delivery) Expand(location *time.Location) ([]ScheduledDelivery, error) {
	if len(d.Reminders) == 0 && d.DueDate == "" && d.DueTime == "" {
		scheduledAt, err := d.ScheduledAt(location)
		if err != nil {
			return nil, err
		}

		return []ScheduledDelivery{
			{
				StateKey:    d.StateKey(),
				DeliveryID:  d.ID,
				UserID:      d.UserID,
				Value:       d.Value,
				Message:     d.Message,
				ScheduledAt: scheduledAt,
				Date:        d.Date,
				Time:        d.Time,
			},
		}, nil
	}

	scheduledDeliveries := make([]ScheduledDelivery, 0, len(d.Reminders))
	for _, reminder := range d.Reminders {
		scheduledAt, err := reminder.ScheduledAt(d.DueDate, location)
		if err != nil {
			return nil, err
		}

		scheduledDeliveries = append(scheduledDeliveries, ScheduledDelivery{
			StateKey:      d.ReminderStateKey(reminder),
			DeliveryID:    d.ID,
			UserID:        d.UserID,
			Value:         d.Value,
			Message:       reminder.Message,
			ScheduledAt:   scheduledAt,
			Date:          scheduledAt.Format("2006-01-02"),
			Time:          scheduledAt.Format("15:04"),
			DueDate:       d.DueDate,
			DueTime:       d.DueTime,
			ReminderID:    reminder.ID,
			ReminderName:  reminder.Name,
			DaysBeforeDue: reminder.DaysBeforeDue,
		})
	}

	return scheduledDeliveries, nil
}

func (d Delivery) ReminderStateKey(reminder Reminder) string {
	if d.ID != "" {
		return "reminder:" + d.ID + ":" + reminder.keyPart()
	}

	sum := sha256.Sum256([]byte(strings.Join([]string{d.UserID, d.DueDate, reminder.keyPart()}, "|")))
	return "reminder:auto:" + hex.EncodeToString(sum[:])
}

func (r Reminder) ScheduledAt(dueDate string, location *time.Location) (time.Time, error) {
	dueDay, err := time.ParseInLocation("2006-01-02", dueDate, location)
	if err != nil {
		return time.Time{}, err
	}

	reminderTime, err := time.ParseInLocation("15:04", r.Time, location)
	if err != nil {
		return time.Time{}, err
	}

	scheduledDay := dueDay.AddDate(0, 0, -r.DaysBeforeDue)
	return time.Date(
		scheduledDay.Year(),
		scheduledDay.Month(),
		scheduledDay.Day(),
		reminderTime.Hour(),
		reminderTime.Minute(),
		0,
		0,
		location,
	), nil
}

func (r Reminder) keyPart() string {
	if r.ID != "" {
		return "id:" + r.ID
	}

	return strings.Join([]string{r.Name, strconv.Itoa(r.DaysBeforeDue), r.Time}, "|")
}

func (d ScheduledDelivery) RenderMessage(template string) string {
	selectedTemplate := template
	if d.Message != "" {
		selectedTemplate = d.Message
	}

	replacer := strings.NewReplacer(
		"{{value}}", d.Value,
		"{{userId}}", d.UserID,
		"{{date}}", d.Date,
		"{{time}}", d.Time,
		"{{dueDate}}", d.DueDate,
		"{{dueTime}}", d.DueTime,
		"{{reminderName}}", d.ReminderName,
		"{{daysBeforeDue}}", strconv.Itoa(d.DaysBeforeDue),
	)

	return replacer.Replace(selectedTemplate)
}

func ParseHexColor(value string) (int, error) {
	cleaned := strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(cleaned) != 6 {
		return 0, fmt.Errorf("must be a 6-digit hex color")
	}

	parsed, err := hex.DecodeString(cleaned)
	if err != nil {
		return 0, fmt.Errorf("must be valid hexadecimal")
	}

	return int(parsed[0])<<16 | int(parsed[1])<<8 | int(parsed[2]), nil
}
