package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"paperless-gpt/ocr"

	"github.com/tmc/langchaingo/llms"
)

const mistralChatTimeout = 3 * time.Minute

var mistralChatEndpoint = "https://api.mistral.ai/v1/chat/completions"

// mistralChatLLM talks to Mistral's chat API directly. langchaingo's Mistral
// client builds on mistral-go, which can only read content that is a plain
// string, not the chunk array reasoning models may answer with. It also
// ignores the context and drops Retry-After, and it forces temperature 1 and
// max_tokens 4000 onto every request. Retries are left to RateLimitedLLM.
type mistralChatLLM struct {
	apiKey string
	model  string
	client *http.Client
}

func newMistralChatLLM(apiKey, model string) *mistralChatLLM {
	return &mistralChatLLM{apiKey: apiKey, model: model, client: &http.Client{Timeout: mistralChatTimeout}}
}

type mistralChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type mistralChatRequest struct {
	Model       string               `json:"model"`
	Messages    []mistralChatMessage `json:"messages"`
	MaxTokens   int                  `json:"max_tokens,omitempty"`
	Temperature float64              `json:"temperature,omitempty"`
}

type mistralChatResponse struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// Call implements the llms.Model interface.
func (m *mistralChatLLM) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return llms.GenerateFromSinglePrompt(ctx, m, prompt, options...)
}

// GenerateContent implements the llms.Model interface. A reply without usable
// text is an error, not an empty suggestion: nobody reviews the result, and a
// document whose fields all came back empty would otherwise count as done.
func (m *mistralChatLLM) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	var callOptions llms.CallOptions
	for _, option := range options {
		option(&callOptions)
	}
	chatMessages, err := mistralChatMessages(messages)
	if err != nil {
		return nil, err
	}
	model := m.model
	if callOptions.Model != "" {
		model = callOptions.Model
	}
	body, err := json.Marshal(mistralChatRequest{
		Model:       model,
		Messages:    chatMessages,
		MaxTokens:   callOptions.MaxTokens,
		Temperature: callOptions.Temperature,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mistralChatEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mistral chat request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading mistral chat response failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &ocr.HTTPStatusError{
			Message:    fmt.Sprintf("mistral chat request failed with status: %d, response: %s", resp.StatusCode, truncateForLog(string(responseBody), 500)),
			StatusCode: resp.StatusCode,
			After:      parseRetryAfterSeconds(resp.Header.Get("Retry-After")),
		}
	}

	var parsed mistralChatResponse
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		return nil, fmt.Errorf("parsing mistral chat response failed: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.New("mistral chat response has no choices")
	}
	choice := parsed.Choices[0]
	text, err := mistralContentText(choice.Message.Content)
	if err != nil {
		return nil, err
	}
	if choice.FinishReason == "length" {
		return nil, errors.New("mistral chat response was cut off at the token limit")
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("mistral chat response has no text (finish reason %q)", choice.FinishReason)
	}
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: text, StopReason: choice.FinishReason}}}, nil
}

func mistralChatMessages(messages []llms.MessageContent) ([]mistralChatMessage, error) {
	chatMessages := make([]mistralChatMessage, 0, len(messages))
	for _, message := range messages {
		role := "user"
		switch message.Role {
		case llms.ChatMessageTypeSystem:
			role = "system"
		case llms.ChatMessageTypeAI:
			role = "assistant"
		}
		for _, part := range message.Parts {
			text, ok := part.(llms.TextContent)
			if !ok {
				return nil, fmt.Errorf("mistral chat supports text only, got %T", part)
			}
			chatMessages = append(chatMessages, mistralChatMessage{Role: role, Content: text.Text})
		}
	}
	return chatMessages, nil
}

// mistralContentText reads a message's content, which is either a string or
// an array of chunks. Only text chunks count; thinking chunks are reasoning.
func mistralContentText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var chunks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &chunks); err != nil {
		return "", fmt.Errorf("mistral chat content has an unknown format: %w", err)
	}
	var builder strings.Builder
	for _, chunk := range chunks {
		if chunk.Type == "text" {
			builder.WriteString(chunk.Text)
		}
	}
	return builder.String(), nil
}

func parseRetryAfterSeconds(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
