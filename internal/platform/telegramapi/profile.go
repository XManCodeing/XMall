package telegramapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"unicode/utf8"
)

// SetMyName changes the bot's display name for an optional language.
func (c *Client) SetMyName(ctx context.Context, name, languageCode string) error {
	if utf8.RuneCountInString(name) > 64 {
		return invalid("invalid_content")
	}
	var result bool
	err := c.callJSON(ctx, "setMyName", struct {
		Name         string `json:"name"`
		LanguageCode string `json:"language_code,omitempty"`
	}{name, languageCode}, &result)
	if err == nil && !result {
		return errors.New("telegram: setMyName refused")
	}
	return err
}

// SetMyDescription changes the bot description shown in an empty chat.
func (c *Client) SetMyDescription(ctx context.Context, description, languageCode string) error {
	if utf8.RuneCountInString(description) > 512 {
		return invalid("invalid_content")
	}
	var result bool
	err := c.callJSON(ctx, "setMyDescription", struct {
		Description  string `json:"description"`
		LanguageCode string `json:"language_code,omitempty"`
	}{description, languageCode}, &result)
	if err == nil && !result {
		return errors.New("telegram: setMyDescription refused")
	}
	return err
}

// SetMyShortDescription changes the short description on the bot profile page.
func (c *Client) SetMyShortDescription(ctx context.Context, shortDescription, languageCode string) error {
	if utf8.RuneCountInString(shortDescription) > 120 {
		return invalid("invalid_content")
	}
	var result bool
	err := c.callJSON(ctx, "setMyShortDescription", struct {
		ShortDescription string `json:"short_description"`
		LanguageCode     string `json:"language_code,omitempty"`
	}{shortDescription, languageCode}, &result)
	if err == nil && !result {
		return errors.New("telegram: setMyShortDescription refused")
	}
	return err
}

// SetMyProfilePhoto uploads a new static JPEG profile photo.
func (c *Client) SetMyProfilePhoto(ctx context.Context, jpeg []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(jpeg) < 3 || jpeg[0] != 0xff || jpeg[1] != 0xd8 || len(jpeg) > MaxUploadBytes {
		return invalid("invalid_media")
	}
	return c.uploadProfilePhoto(ctx, map[string]interface{}{"type": "static", "photo": "attach://profile_photo"}, "profile.jpg", "image/jpeg", jpeg)
}

// SetMyAnimatedProfilePhoto uploads a square MPEG4 animated profile photo.
func (c *Client) SetMyAnimatedProfilePhoto(ctx context.Context, mp4 []byte, mainFrameSeconds float64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(mp4) < 12 || string(mp4[4:8]) != "ftyp" || len(mp4) > MaxUploadBytes || mainFrameSeconds < 0 {
		return invalid("invalid_media")
	}
	descriptor := map[string]interface{}{"type": "animated", "animation": "attach://profile_photo"}
	if mainFrameSeconds > 0 {
		descriptor["main_frame_timestamp"] = mainFrameSeconds
	}
	return c.uploadProfilePhoto(ctx, descriptor, "profile.mp4", "video/mp4", mp4)
}

func (c *Client) uploadProfilePhoto(ctx context.Context, descriptor map[string]interface{}, filename, contentType string, data []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	// The file part must not share the "photo" field name with the JSON descriptor.
	payload, err := json.Marshal(descriptor)
	if err != nil {
		return errors.New("telegram: cannot encode profile photo")
	}
	if err := writer.WriteField("photo", string(payload)); err != nil {
		return errors.New("telegram: cannot encode profile photo")
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "profile_photo", "filename": filename}))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return errors.New("telegram: cannot encode profile photo")
	}
	if _, err := part.Write(data); err != nil {
		return errors.New("telegram: cannot encode profile photo")
	}
	if err := writer.Close(); err != nil {
		return errors.New("telegram: cannot encode profile photo")
	}
	var result bool
	if err := c.call(ctx, "setMyProfilePhoto", writer.FormDataContentType(), &body, &result); err != nil {
		return err
	}
	if !result {
		return errors.New("telegram: setMyProfilePhoto refused")
	}
	return nil
}

// RemoveMyProfilePhoto clears the bot profile photo.
func (c *Client) RemoveMyProfilePhoto(ctx context.Context) error {
	var result bool
	err := c.callJSON(ctx, "removeMyProfilePhoto", struct{}{}, &result)
	if err == nil && !result {
		return errors.New("telegram: removeMyProfilePhoto refused")
	}
	return err
}

// TruncateRunes shortens s to at most n runes.
func TruncateRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
