package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"fuck-the-bot/internal/model"
	"fuck-the-bot/internal/view"
)

const endpoint = "https://api.telegram.org/bot"
const maxPhotoBytes = 5 << 20
const maxVoiceUploadBytes = 10 << 20

type Client struct {
	http  *http.Client
	token string
}

type replyParameters struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
}

type responseParameters struct {
	MigrateToChatID int64 `json:"migrate_to_chat_id"`
}

func responseError(status int, description string, params responseParameters, method string) error {
	message := fmt.Sprintf("Telegram %s HTTP %d: %s", method, status, description)
	if params.MigrateToChatID != 0 || strings.Contains(strings.ToLower(description), "group chat was upgraded to a supergroup chat") {
		return fmt.Errorf("%w: %s", model.ErrChatMigrated, message)
	}
	return fmt.Errorf("%s", message)
}

func replyTo(messageID int64) *replyParameters {
	if messageID <= 0 {
		return nil
	}
	return &replyParameters{MessageID: messageID, AllowSendingWithoutReply: true}
}

func New(token string) *Client {
	return &Client{http: &http.Client{Timeout: 45 * time.Second}, token: token}
}

func (c *Client) call(ctx context.Context, method string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %s", strings.ReplaceAll(err.Error(), c.token, "[redacted]"))
	}
	defer resp.Body.Close()
	var result struct {
		OK          bool               `json:"ok"`
		Description string             `json:"description"`
		Result      json.RawMessage    `json:"result"`
		Parameters  responseParameters `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("decode Telegram response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		return responseError(resp.StatusCode, result.Description, result.Parameters, method)
	}
	return json.Unmarshal(result.Result, output)
}

func (c *Client) GetMe(ctx context.Context) (model.User, error) {
	var me model.User
	err := c.call(ctx, "getMe", struct{}{}, &me)
	return me, err
}

func (c *Client) GetChatMember(ctx context.Context, chatID, userID int64) (model.ChatMember, error) {
	var member model.ChatMember
	err := c.call(ctx, "getChatMember", struct {
		ChatID int64 `json:"chat_id"`
		UserID int64 `json:"user_id"`
	}{chatID, userID}, &member)
	return member, err
}

func (c *Client) ForwardMessage(ctx context.Context, original model.Message) (int64, error) {
	input := struct {
		ChatID              int64 `json:"chat_id"`
		MessageThreadID     int64 `json:"message_thread_id,omitempty"`
		FromChatID          int64 `json:"from_chat_id"`
		MessageID           int64 `json:"message_id"`
		DisableNotification bool  `json:"disable_notification"`
		ProtectContent      bool  `json:"protect_content"`
	}{original.Chat.ID, original.MessageThreadID, original.Chat.ID, original.MessageID, true, true}
	var forwarded struct {
		MessageID int64 `json:"message_id"`
	}
	if err := c.call(ctx, "forwardMessage", input, &forwarded); err != nil {
		if strings.Contains(err.Error(), "Telegram forwardMessage HTTP 400") {
			return 0, fmt.Errorf("%w: %v", model.ErrMessageCannotForward, err)
		}
		return 0, err
	}
	if forwarded.MessageID <= 0 {
		return 0, fmt.Errorf("Telegram forwardMessage returned no message ID")
	}
	return forwarded.MessageID, nil
}

func (c *Client) RestrictChatMember(ctx context.Context, chatID, userID int64, until time.Time) error {
	input := struct {
		ChatID                        int64           `json:"chat_id"`
		UserID                        int64           `json:"user_id"`
		Permissions                   map[string]bool `json:"permissions"`
		UseIndependentChatPermissions bool            `json:"use_independent_chat_permissions"`
		UntilDate                     int64           `json:"until_date"`
	}{chatID, userID, map[string]bool{"can_send_messages": false}, true, until.Unix()}
	var ok bool
	if err := c.call(ctx, "restrictChatMember", input, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Telegram did not confirm member restriction")
	}
	return nil
}

func (c *Client) UnrestrictChatMember(ctx context.Context, chatID, userID int64) error {
	permissions := map[string]bool{
		"can_send_messages":         true,
		"can_send_audios":           true,
		"can_send_documents":        true,
		"can_send_photos":           true,
		"can_send_videos":           true,
		"can_send_video_notes":      true,
		"can_send_voice_notes":      true,
		"can_send_polls":            true,
		"can_send_other_messages":   true,
		"can_add_web_page_previews": true,
	}
	var ok bool
	if err := c.call(ctx, "restrictChatMember", struct {
		ChatID      int64           `json:"chat_id"`
		UserID      int64           `json:"user_id"`
		Permissions map[string]bool `json:"permissions"`
	}{chatID, userID, permissions}, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Telegram did not confirm member unrestriction")
	}
	return nil
}

func (c *Client) UnbanChatMember(ctx context.Context, chatID, userID int64) error {
	var ok bool
	if err := c.call(ctx, "unbanChatMember", struct {
		ChatID       int64 `json:"chat_id"`
		UserID       int64 `json:"user_id"`
		OnlyIfBanned bool  `json:"only_if_banned"`
	}{chatID, userID, true}, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Telegram did not confirm member unban")
	}
	return nil
}

func (c *Client) KickChatMember(ctx context.Context, chatID, userID int64) error {
	// A short ban removes the user, then lets them rejoin later by invitation.
	input := struct {
		ChatID    int64 `json:"chat_id"`
		UserID    int64 `json:"user_id"`
		UntilDate int64 `json:"until_date"`
	}{chatID, userID, time.Now().Add(time.Minute).Unix()}
	var ok bool
	if err := c.call(ctx, "banChatMember", input, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Telegram did not confirm member removal")
	}
	return nil
}

func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	var ok bool
	if err := c.call(ctx, "deleteMessage", struct {
		ChatID    int64 `json:"chat_id"`
		MessageID int64 `json:"message_id"`
	}{chatID, messageID}, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Telegram did not confirm message deletion")
	}
	return nil
}

func (c *Client) DownloadPhoto(ctx context.Context, sizes []model.PhotoSize) ([]byte, error) {
	var chosen model.PhotoSize
	var chosenPixels int64
	for _, size := range sizes {
		if size.FileID == "" || size.FileSize > maxPhotoBytes {
			continue
		}
		pixels := int64(size.Width) * int64(size.Height)
		if chosen.FileID == "" || pixels > chosenPixels {
			chosen, chosenPixels = size, pixels
		}
	}
	if chosen.FileID == "" {
		return nil, fmt.Errorf("photo has no downloadable size up to %d bytes", maxPhotoBytes)
	}
	var file struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err := c.call(ctx, "getFile", struct {
		FileID string `json:"file_id"`
	}{chosen.FileID}, &file); err != nil {
		return nil, fmt.Errorf("get Telegram photo file: %w", err)
	}
	if file.FileSize > maxPhotoBytes {
		return nil, fmt.Errorf("Telegram photo exceeds %d bytes", maxPhotoBytes)
	}
	if file.FilePath == "" || path.Clean(file.FilePath) != file.FilePath || strings.HasPrefix(file.FilePath, "/") || strings.ContainsAny(file.FilePath, "?#:") {
		return nil, fmt.Errorf("Telegram returned an invalid photo path")
	}
	fileURL := url.URL{Scheme: "https", Host: "api.telegram.org", Path: "/file/bot" + c.token + "/" + file.FilePath}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download Telegram photo: %s", strings.ReplaceAll(err.Error(), c.token, "[redacted]"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download Telegram photo: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxPhotoBytes {
		return nil, fmt.Errorf("Telegram photo exceeds %d bytes", maxPhotoBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Telegram photo: %w", err)
	}
	if len(data) == 0 || len(data) > maxPhotoBytes {
		return nil, fmt.Errorf("Telegram photo is empty or exceeds %d bytes", maxPhotoBytes)
	}
	return data, nil
}

func (c *Client) SendMessage(ctx context.Context, original model.Message, answer string) error {
	for i, chunk := range view.TelegramChunks(answer) {
		input := struct {
			ChatID          int64            `json:"chat_id"`
			Text            string           `json:"text"`
			MessageThreadID int64            `json:"message_thread_id,omitempty"`
			ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
		}{ChatID: original.Chat.ID, Text: chunk, MessageThreadID: original.MessageThreadID}
		if i == 0 {
			input.ReplyParameters = replyTo(original.MessageID)
		}
		var sent json.RawMessage
		if err := c.call(ctx, "sendMessage", input, &sent); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) SendContactMention(ctx context.Context, original model.Message, contact model.Contact, introduction string) error {
	expectedLink := fmt.Sprintf("tg://user?id=%d", contact.UserID)
	if contact.UserID <= 0 || contact.Link != expectedLink {
		return fmt.Errorf("invalid contact mention link")
	}
	name := strings.TrimSpace(contact.Name)
	if contact.Username != "" && !strings.EqualFold(name, "@"+contact.Username) {
		name += " (@" + contact.Username + ")"
	}
	if name == "" {
		name = fmt.Sprintf("user#%d", contact.UserID)
	}
	text := strings.TrimSpace(html.EscapeString(introduction))
	if text != "" {
		text += " "
	}
	text += `<a href="` + expectedLink + `">` + html.EscapeString(name) + `</a>`
	input := struct {
		ChatID          int64            `json:"chat_id"`
		Text            string           `json:"text"`
		ParseMode       string           `json:"parse_mode"`
		MessageThreadID int64            `json:"message_thread_id,omitempty"`
		ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
	}{ChatID: original.Chat.ID, Text: text, ParseMode: "HTML", MessageThreadID: original.MessageThreadID, ReplyParameters: replyTo(original.MessageID)}
	var sent json.RawMessage
	return c.call(ctx, "sendMessage", input, &sent)
}

func (c *Client) SendPoll(ctx context.Context, original model.Message, poll model.Poll) error {
	type pollOption struct {
		Text string `json:"text"`
	}
	input := struct {
		ChatID          int64            `json:"chat_id"`
		MessageThreadID int64            `json:"message_thread_id,omitempty"`
		Question        string           `json:"question"`
		Options         []pollOption     `json:"options"`
		ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
	}{ChatID: original.Chat.ID, MessageThreadID: original.MessageThreadID, Question: poll.Question, ReplyParameters: replyTo(original.MessageID)}
	for _, option := range poll.Options {
		input.Options = append(input.Options, pollOption{Text: option})
	}
	var sent json.RawMessage
	return c.call(ctx, "sendPoll", input, &sent)
}

func (c *Client) SendUnbanPoll(ctx context.Context, original model.Message, targetName string) (int64, error) {
	question := "Разблокировать " + model.CompactText(targetName, 240) + "? Голосование 3 минуты"
	input := struct {
		ChatID          int64  `json:"chat_id"`
		MessageThreadID int64  `json:"message_thread_id,omitempty"`
		Question        string `json:"question"`
		Options         []struct {
			Text string `json:"text"`
		} `json:"options"`
		IsAnonymous           bool             `json:"is_anonymous"`
		AllowsMultipleAnswers bool             `json:"allows_multiple_answers"`
		ReplyParameters       *replyParameters `json:"reply_parameters,omitempty"`
	}{ChatID: original.Chat.ID, MessageThreadID: original.MessageThreadID, Question: question, IsAnonymous: true, ReplyParameters: replyTo(original.MessageID)}
	input.Options = []struct {
		Text string `json:"text"`
	}{{"За"}, {"Против"}}
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	if err := c.call(ctx, "sendPoll", input, &sent); err != nil {
		return 0, err
	}
	if sent.MessageID <= 0 {
		return 0, fmt.Errorf("Telegram sendPoll returned no message ID")
	}
	return sent.MessageID, nil
}

func (c *Client) StopUnbanPoll(ctx context.Context, chatID, messageID int64) (model.UnbanPollResult, error) {
	var stopped struct {
		Options []struct {
			Text       string `json:"text"`
			VoterCount int    `json:"voter_count"`
		} `json:"options"`
	}
	if err := c.call(ctx, "stopPoll", struct {
		ChatID    int64 `json:"chat_id"`
		MessageID int64 `json:"message_id"`
	}{chatID, messageID}, &stopped); err != nil {
		return model.UnbanPollResult{}, err
	}
	if len(stopped.Options) != 2 || stopped.Options[0].Text != "За" || stopped.Options[1].Text != "Против" || stopped.Options[0].VoterCount < 0 || stopped.Options[1].VoterCount < 0 {
		return model.UnbanPollResult{}, fmt.Errorf("Telegram stopPoll returned unexpected options")
	}
	return model.UnbanPollResult{Yes: stopped.Options[0].VoterCount, No: stopped.Options[1].VoterCount}, nil
}

func (c *Client) SendAudio(ctx context.Context, original model.Message, audioURL, title string) error {
	input := struct {
		ChatID          int64            `json:"chat_id"`
		MessageThreadID int64            `json:"message_thread_id,omitempty"`
		Audio           string           `json:"audio"`
		Title           string           `json:"title,omitempty"`
		ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
	}{
		ChatID: original.Chat.ID, MessageThreadID: original.MessageThreadID,
		Audio: audioURL, Title: model.CompactText(title, 64), ReplyParameters: replyTo(original.MessageID),
	}
	var sent json.RawMessage
	return c.call(ctx, "sendAudio", input, &sent)
}

func (c *Client) SendVoice(ctx context.Context, original model.Message, audio []byte) error {
	if len(audio) == 0 || len(audio) > maxVoiceUploadBytes {
		return fmt.Errorf("voice audio must be 1-%d bytes", maxVoiceUploadBytes)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("chat_id", strconv.FormatInt(original.Chat.ID, 10)); err != nil {
		return err
	}
	if original.MessageThreadID > 0 {
		if err := form.WriteField("message_thread_id", strconv.FormatInt(original.MessageThreadID, 10)); err != nil {
			return err
		}
	}
	if reply := replyTo(original.MessageID); reply != nil {
		encoded, err := json.Marshal(reply)
		if err != nil {
			return err
		}
		if err := form.WriteField("reply_parameters", string(encoded)); err != nil {
			return err
		}
	}
	part, err := form.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="voice"; filename="voice.mp3"`},
		"Content-Type":        {"audio/mpeg"},
	})
	if err != nil {
		return err
	}
	if _, err := part.Write(audio); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+c.token+"/sendVoice", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send Telegram voice: %s", strings.ReplaceAll(err.Error(), c.token, "[redacted]"))
	}
	defer resp.Body.Close()
	var result struct {
		OK          bool               `json:"ok"`
		Description string             `json:"description"`
		Parameters  responseParameters `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("decode Telegram voice response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		return responseError(resp.StatusCode, result.Description, result.Parameters, "sendVoice")
	}
	return nil
}

func (c *Client) SetReaction(ctx context.Context, target model.Message, emoji string) error {
	input := struct {
		ChatID    int64 `json:"chat_id"`
		MessageID int64 `json:"message_id"`
		Reaction  []struct {
			Type  string `json:"type"`
			Emoji string `json:"emoji"`
		} `json:"reaction"`
	}{ChatID: target.Chat.ID, MessageID: target.MessageID}
	input.Reaction = append(input.Reaction, struct {
		Type  string `json:"type"`
		Emoji string `json:"emoji"`
	}{Type: "emoji", Emoji: emoji})
	var ok bool
	if err := c.call(ctx, "setMessageReaction", input, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Telegram did not confirm reaction")
	}
	return nil
}
