package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTTSProxy(t *testing.T) {
	models := []string{"", "gemini-3.8-flash-tts", "gemini-3.8-flash-lite-tts",
		"gemini-3.1-flash-tts-preview", "gemini-2.5-flash-tts", "gemini-2.5-pro-tts",
		"gemini-2.5-flash-lite-preview-tts"}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			audio := "RIFF-test-audio"
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				encoded := base64.StdEncoding.EncodeToString([]byte(audio))
				response := `{"audioContent":"` + encoded + `"}`
				if model == "" || strings.HasPrefix(model, "gemini-3.8-") {
					expected := firstNonEmpty(model, "gemini-3.8-flash-tts")
					if r.URL.Host != "aiplatform.googleapis.com" || !strings.HasSuffix(r.URL.Path, "/"+expected+":generateContent") {
						t.Fatalf("wrong 3.8 endpoint: %s", r.URL)
					}
					part := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)
					config := body["generationConfig"].(map[string]any)["speechConfig"].(map[string]any)
					if part["text"] != "Hello" || part["speechMetadata"].(map[string]any)["style"] != "Warm" ||
						config["voiceConfig"].(map[string]any)["voice"] != "Kore" || config["languageCode"] != "en-US" {
						t.Fatalf("wrong 3.8 payload: %v", body)
					}
					// A text part before the audio must not hide the WAV.
					response = `{"candidates":[{"content":{"parts":[{"text":""},{"inlineData":{"data":"` + encoded + `"}}]}}]}`
				} else {
					voice := body["voice"].(map[string]any)
					if r.URL.String() != ttsEndpoint || voice["modelName"] != model || voice["name"] != "Kore" ||
						body["input"].(map[string]any)["prompt"] != "Warm" {
						t.Fatalf("wrong Cloud TTS request: %s %v", r.URL, body)
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response))}, nil
			})}
			payload, _ := json.Marshal(synthesizeRequest{Text: "Hello", Prompt: " Warm ", Voice: "Kore", LanguageCode: "en-US", Model: model})
			w := httptest.NewRecorder()
			newMux(client, "test-project").ServeHTTP(w, httptest.NewRequest("POST", "/api/synthesize", strings.NewReader(string(payload))))
			if w.Code != 200 || w.Body.String() != audio || w.Header().Get("Content-Type") != "audio/wav" {
				t.Fatalf("bad audio response: %d %s", w.Code, w.Body)
			}
		})
	}

	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" && (r.URL.Path != "/v1beta1/projects/test-project/locations/global/voices" ||
			r.URL.Query().Get("pageToken") != "next page" || r.URL.Query().Get("type") != "prebuilt") {
			t.Fatalf("bad library request: %s", r.URL)
		}
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"permission denied"}}`))}, nil
	})}
	w := httptest.NewRecorder()
	newMux(client, "test-project").ServeHTTP(w, httptest.NewRequest("GET", "/api/voices?pageToken=next%20page", nil))
	if w.Code != 403 || !strings.Contains(w.Body.String(), "permission denied") {
		t.Fatalf("library error lost: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct{ project, model string }{{"", "gemini-3.8-flash-tts"}, {"test-project", "invalid"}} {
		payload, _ := json.Marshal(synthesizeRequest{Text: "Hello", Voice: "Kore", LanguageCode: "en-US", Model: tc.model})
		w := httptest.NewRecorder()
		newMux(client, tc.project).ServeHTTP(w, httptest.NewRequest("POST", "/api/synthesize", strings.NewReader(string(payload))))
		if w.Code != 400 {
			t.Fatalf("invalid request accepted: %d", w.Code)
		}
	}
}
