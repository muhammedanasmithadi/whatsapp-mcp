package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

var privacySettingTypes = []types.PrivacySettingType{
	types.PrivacySettingTypeGroupAdd,
	types.PrivacySettingTypeLastSeen,
	types.PrivacySettingTypeStatus,
	types.PrivacySettingTypeProfile,
	types.PrivacySettingTypeReadReceipts,
	types.PrivacySettingTypeOnline,
	types.PrivacySettingTypeCallAdd,
	types.PrivacySettingTypeMessages,
	types.PrivacySettingTypeDefense,
	types.PrivacySettingTypeStickers,
}

var privacySettingValues = []types.PrivacySetting{
	types.PrivacySettingAll,
	types.PrivacySettingContacts,
	types.PrivacySettingContactAllowlist,
	types.PrivacySettingContactBlacklist,
	types.PrivacySettingMatchLastSeen,
	types.PrivacySettingKnown,
	types.PrivacySettingNone,
	types.PrivacySettingOnStandard,
	types.PrivacySettingOff,
}

func joinNames[T ~string](vals []T) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = string(v)
	}
	return strings.Join(parts, ", ")
}

func parsePresence(state string) (types.Presence, error) {
	switch state {
	case string(types.PresenceAvailable):
		return types.PresenceAvailable, nil
	case string(types.PresenceUnavailable):
		return types.PresenceUnavailable, nil
	default:
		return "", fmt.Errorf("invalid state %q: must be one of available, unavailable", state)
	}
}

func (c *Client) SendPresence(ctx context.Context, state string) error {
	p, err := parsePresence(state)
	if err != nil {
		return err
	}
	if !c.socketConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if err := c.wa.SendPresence(ctx, p); err != nil {
		return fmt.Errorf("send presence: %w", err)
	}
	c.ownPresenceMu.Lock()
	c.ownPresence = p
	c.ownPresenceMu.Unlock()
	return nil
}

func (c *Client) GetPrivacySettings(ctx context.Context) (string, error) {
	if !c.socketConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	settings, err := c.wa.GetPrivacySettings(ctx)
	if err != nil {
		return "", fmt.Errorf("get privacy settings: %w", err)
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("marshal privacy settings: %w", err)
	}
	return string(b), nil
}

func validatePrivacySettingName(name string) error {
	for _, n := range privacySettingTypes {
		if string(n) == name {
			return nil
		}
	}
	return fmt.Errorf("invalid name %q: must be one of %s", name, joinNames(privacySettingTypes))
}

func validatePrivacySettingValue(value string) error {
	for _, v := range privacySettingValues {
		if string(v) == value {
			return nil
		}
	}
	return fmt.Errorf("invalid value %q: must be one of %s", value, joinNames(privacySettingValues))
}

func (c *Client) SetPrivacySetting(ctx context.Context, name, value string) (string, error) {
	if err := validatePrivacySettingName(name); err != nil {
		return "", err
	}
	if err := validatePrivacySettingValue(value); err != nil {
		return "", err
	}
	if !c.socketConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	settings, err := c.wa.SetPrivacySetting(ctx, types.PrivacySettingType(name), types.PrivacySetting(value))
	if err != nil {
		return "", fmt.Errorf("set privacy setting: %w", err)
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("marshal privacy settings: %w", err)
	}
	return string(b), nil
}

func (c *Client) SetStatusMessage(ctx context.Context, text string) error {
	if !c.socketConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if err := c.wa.SetStatusMessage(ctx, types.SetStatusInput{Text: &text}); err != nil {
		return fmt.Errorf("set status message: %w", err)
	}
	return nil
}
