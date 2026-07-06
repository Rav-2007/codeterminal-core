package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// requestTimeout bounds a single prompt's total call to the model API so a
// stalled upstream can't hang a connection (and its goroutine) forever.
const requestTimeout = 5 * time.Minute

// chatCompletionsPath is appended to CODETERMINAL_API_BASE for every call.
const chatCompletionsPath = "/chat/completions"

// SSE lines are almost always small; the scanner starts with a modest
// buffer and grows up to sseMaxLineSize only if a provider sends an
// unusually large single chunk.
const (
	sseInitialBufferSize = 64 * 1024
	sseMaxLineSize       = 1024 * 1024
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatCompletionChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// buildChatMessages assembles the message list sent to the model: an
// optional leading "system" message, then exactly one "user" message. It is
// extracted from streamCompletion so tests can assert directly on the
// constructed request structure — in particular, that retrieved context
// (folded into prompt by the caller, see buildAugmentedUserMessage) always
// lands in the "user" message and never in "system".
func buildChatMessages(systemPrompt, prompt string) []chatMessage {
	var messages []chatMessage
	if systemPrompt != "" {
		messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, chatMessage{Role: "user", Content: prompt})
	return messages
}

// streamCompletion calls an OpenAI-compatible POST {apiBase}/chat/completions
// endpoint with stream=true and invokes onToken for each content fragment as
// it arrives over the SSE response. It never buffers the full reply.
// systemPrompt, if non-empty, is sent as the leading "system" message.
func streamCompletion(ctx context.Context, apiBase, apiKey, model, systemPrompt, prompt string, onToken func(string) error) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	messages := buildChatMessages(systemPrompt, prompt)

	reqBody, err := json.Marshal(chatCompletionRequest{
		Model:    model,
		Messages: messages,
		Stream:   true,
	})
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}

	url := strings.TrimRight(apiBase, "/") + chatCompletionsPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("calling model API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("model API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, sseInitialBufferSize), sseMaxLineSize)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil
		}

		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed / keep-alive lines
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		content := chunk.Choices[0].Delta.Content
		if content == "" {
			continue
		}
		if err := onToken(content); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stream: %w", err)
	}
	return nil
}
