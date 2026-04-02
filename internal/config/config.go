package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

var discordUserIDPattern = regexp.MustCompile(`^\d{10,20}$`)

type Config struct {
	Discord    Discord    `toml:"discord"`
	Runtime    Runtime    `toml:"runtime"`
	Embed      Embed      `toml:"embed"`
	Deliveries []Delivery `toml:"deliveries"`
}

type Discord struct {
	BotToken       string   `toml:"bot_token"`
	GuildIDs       []string `toml:"guild_ids"`
	AllowedRoleIDs []string `toml:"allowed_role_ids"`
}

type Runtime struct {
	Timezone            string `toml:"timezone"`
	PollIntervalSeconds int    `toml:"poll_interval_seconds"`
	StatePath           string `toml:"state_path"`
}

type Embed struct {
	Title               string `toml:"title"`
	DescriptionTemplate string `toml:"description_template"`
	Footer              string `toml:"footer"`
	Color               string `toml:"color"`
}

type Delivery struct {
	ID      string `toml:"id,omitempty"`
	UserID  string `toml:"user_id"`
	Date    string `toml:"date"`
	Time    string `toml:"time"`
	Message string `toml:"message,omitempty"`
	Value   string `toml:"value"`
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

	seen := make(map[string]struct{}, len(c.Deliveries))
	for index := range c.Deliveries {
		delivery := &c.Deliveries[index]
		delivery.ID = strings.TrimSpace(delivery.ID)
		delivery.UserID = strings.TrimSpace(delivery.UserID)
		delivery.Date = strings.TrimSpace(delivery.Date)
		delivery.Time = strings.TrimSpace(delivery.Time)
		delivery.Message = strings.TrimSpace(delivery.Message)
		delivery.Value = strings.TrimSpace(delivery.Value)

		if delivery.UserID == "" {
			return fmt.Errorf("deliveries[%d].user_id is required", index)
		}
		if !discordUserIDPattern.MatchString(delivery.UserID) {
			return fmt.Errorf("deliveries[%d].user_id must be a Discord snowflake", index)
		}
		if delivery.Date == "" {
			return fmt.Errorf("deliveries[%d].date is required", index)
		}
		if delivery.Time == "" {
			return fmt.Errorf("deliveries[%d].time is required", index)
		}
		if delivery.Value == "" {
			return fmt.Errorf("deliveries[%d].value is required", index)
		}
		if delivery.Message == "" && c.Embed.DescriptionTemplate == "" {
			return fmt.Errorf("deliveries[%d] requires either message or embed.description_template", index)
		}

		if _, err := delivery.ScheduledAt(location); err != nil {
			return fmt.Errorf("deliveries[%d] has invalid date/time: %w", index, err)
		}

		id := delivery.StateKey()
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate delivery id/state key %q detected", id)
		}
		seen[id] = struct{}{}
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

func (d Delivery) RenderMessage(template string) string {
	selectedTemplate := template
	if d.Message != "" {
		selectedTemplate = d.Message
	}

	replacer := strings.NewReplacer(
		"{{value}}", d.Value,
		"{{userId}}", d.UserID,
		"{{date}}", d.Date,
		"{{time}}", d.Time,
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
