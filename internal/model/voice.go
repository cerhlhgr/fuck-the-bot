package model

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const MaxVoiceTextRunes = 1000
const MaxVoiceInstructionsRunes = 300
const DefaultVoiceSpeaker = "onyx"

type VoiceRequest struct {
	Text         string `json:"text"`
	Speaker      string `json:"speaker"`
	Instructions string `json:"instructions,omitempty"`
}

func ValidateVoiceRequest(raw VoiceRequest) (VoiceRequest, error) {
	raw.Text = strings.TrimSpace(raw.Text)
	raw.Speaker = strings.TrimSpace(raw.Speaker)
	raw.Instructions = strings.TrimSpace(raw.Instructions)
	if raw.Speaker == "" {
		raw.Speaker = DefaultVoiceSpeaker
	}
	if n := utf8.RuneCountInString(raw.Text); n == 0 || n > MaxVoiceTextRunes {
		return VoiceRequest{}, errors.New("voice text must be 1-1000 characters")
	}
	if utf8.RuneCountInString(raw.Instructions) > MaxVoiceInstructionsRunes {
		return VoiceRequest{}, errors.New("voice instructions exceed 300 characters")
	}
	switch raw.Speaker {
	case "alloy", "ash", "ballad", "coral", "echo", "fable", "nova", "onyx", "sage", "shimmer", "verse", "marin", "cedar":
		return raw, nil
	default:
		return VoiceRequest{}, errors.New("unsupported voice speaker")
	}
}
