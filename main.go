package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// ttsEndpoint is the global Cloud Text-to-Speech synthesize endpoint.
const ttsEndpoint = "https://texttospeech.googleapis.com/v1/text:synthesize"

// scope is the OAuth scope required by the synthesize method.
const scope = "https://www.googleapis.com/auth/cloud-platform"

//go:embed static/index.html
var indexHTML []byte

// synthesizeRequest is what the browser sends us.
type synthesizeRequest struct {
	Text         string `json:"text"`
	Prompt       string `json:"prompt"`       // optional style instructions
	Voice        string `json:"voice"`        // e.g. "Kore"
	LanguageCode string `json:"languageCode"` // e.g. "en-US"
	Model        string `json:"model"`        // e.g. "gemini-3.8-flash-tts"
}

// googleRequest mirrors the Cloud TTS request body.
type googleRequest struct {
	Input struct {
		Text   string `json:"text"`
		Prompt string `json:"prompt,omitempty"`
	} `json:"input"`
	Voice struct {
		LanguageCode string `json:"languageCode"`
		Name         string `json:"name"`
		ModelName    string `json:"modelName"`
	} `json:"voice"`
	AudioConfig struct {
		AudioEncoding string `json:"audioEncoding"`
	} `json:"audioConfig"`
}

func main() {
	env := loadEnv(".env")
	project := firstNonEmpty(env["CLOUD_PROJECT_ID"], os.Getenv("CLOUD_PROJECT_ID"))
	credPath := firstNonEmpty(env["GOOGLE_APPLICATION_CREDENTIALS"], os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"), "sa-key.json")

	// Build an auto-refreshing token source from the service-account key.
	// The key file never expires; the access tokens it mints are refreshed
	// automatically by the oauth2 client, so there is nothing to babysit.
	keyBytes, err := os.ReadFile(credPath)
	if err != nil {
		log.Fatalf("cannot read service-account key %q: %v", credPath, err)
	}
	creds, err := google.CredentialsFromJSON(context.Background(), keyBytes, scope)
	if err != nil {
		log.Fatalf("invalid service-account key: %v", err)
	}
	// oauth2 client injects (and refreshes) the Bearer token on every request.
	authClient := oauth2.NewClient(context.Background(), creds.TokenSource)
	authClient.Timeout = 60 * time.Second

	port := firstNonEmpty(os.Getenv("PORT"), "8080")
	log.Printf("TTS server listening on http://localhost:%s  (project=%s, creds=%s)", port, project, credPath)
	if err := http.ListenAndServe(":"+port, newMux(authClient, project)); err != nil {
		log.Fatal(err)
	}
}

func newMux(authClient *http.Client, project string) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	// The extended voice catalog is available to Gemini 3.8 models only.
	mux.HandleFunc("/api/voices", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		if project == "" {
			http.Error(w, "CLOUD_PROJECT_ID is required for the voice library", http.StatusBadRequest)
			return
		}
		query := url.Values{"type": {"prebuilt"}, "pageSize": {"50"}}
		if token := r.URL.Query().Get("pageToken"); token != "" {
			query.Set("pageToken", token)
		}
		endpoint := "https://aiplatform.googleapis.com/v1beta1/projects/" + url.PathEscape(project) + "/locations/global/voices?" + query.Encode()
		upstream, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp, err := authClient.Do(upstream)
		if err != nil {
			http.Error(w, "voice library request failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	mux.HandleFunc("/api/synthesize", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		var req synthesizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			http.Error(w, "text is required", http.StatusBadRequest)
			return
		}
		if req.Voice == "" || req.LanguageCode == "" {
			http.Error(w, "voice and languageCode are required", http.StatusBadRequest)
			return
		}
		if req.Model == "" {
			req.Model = "gemini-3.8-flash-tts"
		}
		switch req.Model {
		case "gemini-3.8-flash-tts", "gemini-3.8-flash-lite-tts",
			"gemini-3.1-flash-tts-preview", "gemini-2.5-flash-tts",
			"gemini-2.5-pro-tts", "gemini-2.5-flash-lite-preview-tts":
		default:
			http.Error(w, "unsupported TTS model", http.StatusBadRequest)
			return
		}

		var gReq googleRequest
		gReq.Input.Text = req.Text
		gReq.Input.Prompt = strings.TrimSpace(req.Prompt)
		gReq.Voice.LanguageCode = req.LanguageCode
		gReq.Voice.Name = req.Voice
		gReq.Voice.ModelName = req.Model
		gReq.AudioConfig.AudioEncoding = "LINEAR16" // returned as a WAV container

		body, _ := json.Marshal(gReq)
		endpoint := ttsEndpoint
		is38 := req.Model == "gemini-3.8-flash-tts" || req.Model == "gemini-3.8-flash-lite-tts"
		if is38 {
			if project == "" {
				http.Error(w, "CLOUD_PROJECT_ID is required for Gemini 3.8", http.StatusBadRequest)
				return
			}
			endpoint = "https://aiplatform.googleapis.com/v1/projects/" + url.PathEscape(project) + "/locations/global/publishers/google/models/" + req.Model + ":generateContent"
			body, _ = json.Marshal(map[string]any{
				"contents": []any{map[string]any{
					"role": "user",
					"parts": []any{map[string]any{
						"text":           req.Text,
						"speechMetadata": map[string]string{"style": strings.TrimSpace(req.Prompt)},
					}},
				}},
				"generationConfig": map[string]any{
					"responseModalities": []string{"AUDIO"},
					"speechConfig": map[string]any{
						"languageCode": req.LanguageCode,
						"voiceConfig":  map[string]string{"voice": req.Voice},
					},
				},
			})
		}

		gHTTP, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		gHTTP.Header.Set("Content-Type", "application/json")

		resp, err := authClient.Do(gHTTP)
		if err != nil {
			http.Error(w, "upstream request failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "could not read audio response: "+err.Error(), http.StatusBadGateway)
			return
		}

		if resp.StatusCode != http.StatusOK {
			// Forward Google's error verbatim so the UI can show the real reason.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(respBody)
			return
		}

		var gResp struct {
			AudioContent string `json:"audioContent"`
			Candidates   []struct {
				Content struct {
					Parts []struct {
						InlineData struct {
							Data string `json:"data"`
						} `json:"inlineData"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		err = json.Unmarshal(respBody, &gResp)
		if is38 && len(gResp.Candidates) > 0 {
			for _, part := range gResp.Candidates[0].Content.Parts {
				if part.InlineData.Data != "" {
					gResp.AudioContent = part.InlineData.Data
					break
				}
			}
		}
		if err != nil || gResp.AudioContent == "" {
			http.Error(w, "no audio in response: "+string(respBody), http.StatusBadGateway)
			return
		}
		audio, err := base64.StdEncoding.DecodeString(gResp.AudioContent)
		if err != nil {
			http.Error(w, "could not decode audio: "+err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Disposition", "inline; filename=\"speech.wav\"")
		w.Write(audio)
	})

	return mux
}

// loadEnv reads a minimal KEY=VALUE .env file. Lines starting with # are ignored.
func loadEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		out[k] = v
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
