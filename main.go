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
	Model        string `json:"model"`        // e.g. "gemini-2.5-flash-tts"
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

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
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
			req.Model = "gemini-2.5-flash-tts"
		}

		var gReq googleRequest
		gReq.Input.Text = req.Text
		gReq.Input.Prompt = strings.TrimSpace(req.Prompt)
		gReq.Voice.LanguageCode = req.LanguageCode
		gReq.Voice.Name = req.Voice
		gReq.Voice.ModelName = req.Model
		gReq.AudioConfig.AudioEncoding = "LINEAR16" // returned as a WAV container

		body, _ := json.Marshal(gReq)

		gHTTP, err := http.NewRequest(http.MethodPost, ttsEndpoint, bytes.NewReader(body))
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
		respBody, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusOK {
			// Forward Google's error verbatim so the UI can show the real reason.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(respBody)
			return
		}

		var gResp struct {
			AudioContent string `json:"audioContent"`
		}
		if err := json.Unmarshal(respBody, &gResp); err != nil || gResp.AudioContent == "" {
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

	port := firstNonEmpty(os.Getenv("PORT"), "8080")
	log.Printf("TTS server listening on http://localhost:%s  (project=%s, creds=%s)", port, project, credPath)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
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
