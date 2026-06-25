# Gemini TTS

A minimalistic web app for text-to-speech using Google Cloud **Gemini-TTS**, with a small Go server.

- Pick a voice by **gender** (Female/Male) → then **speaker name**
- Pick a **language** from a type-to-filter list (89 languages, GA + preview)
- Optional **style prompt** (e.g. *"Read in a calm, warm tone"*) and **model** selection
- Plays the result in the browser

The Go server is a thin proxy: it injects service-account credentials so the
browser never sees a token, calls the Cloud Text-to-Speech `synthesize`
endpoint, and streams back a WAV.

## Prerequisites

- [Go](https://go.dev/dl/) 1.22+
- A Google Cloud project with the **Text-to-Speech API** enabled
- A **service-account JSON key** with the `roles/aiplatform.user` role
  (Gemini-TTS models are served via Vertex AI)

## Setup

1. Create a service account and key (one-time):

   ```bash
   PROJECT=your-gcp-project-id
   gcloud iam service-accounts create tts-sa --project=$PROJECT --display-name="Gemini TTS"
   gcloud projects add-iam-policy-binding $PROJECT \
     --member="serviceAccount:tts-sa@$PROJECT.iam.gserviceaccount.com" \
     --role="roles/aiplatform.user"
   gcloud iam service-accounts keys create sa-key.json \
     --iam-account=tts-sa@$PROJECT.iam.gserviceaccount.com
   ```

   The key file does not expire by default — keep it private (it is gitignored).

2. Configure the environment:

   ```bash
   cp .env-example .env
   ```

   Edit `.env`:

   ```
   CLOUD_PROJECT_ID=your-gcp-project-id
   GOOGLE_APPLICATION_CREDENTIALS=sa-key.json
   ```

## Run

```bash
go run .          # or: go build -o tts_gemini.exe . && ./tts_gemini.exe
```

Open <http://localhost:8080>. Set `PORT` to use a different port.

> `GOOGLE_APPLICATION_CREDENTIALS` is a relative path, so start the server from
> the project directory.

## How it works

| File               | Purpose                                                              |
| ------------------ | ------------------------------------------------------------------- |
| `main.go`          | Go server: serves the UI and proxies `POST /api/synthesize` to GCP. |
| `static/index.html`| Single-page UI (embedded into the binary at build time).            |
| `.env`             | Project ID + path to the service-account key (not committed).       |

Authentication uses `golang.org/x/oauth2/google`, which mints and auto-refreshes
access tokens from the service-account key — there is no token to rotate by hand.

## API

`POST /api/synthesize` → returns `audio/wav`

```json
{
  "text": "Hello world.",
  "prompt": "Read in a calm, warm tone",
  "voice": "Kore",
  "languageCode": "en-US",
  "model": "gemini-2.5-flash-tts"
}
```

Available models: `gemini-2.5-flash-tts` (default), `gemini-2.5-pro-tts`,
`gemini-3.1-flash-tts-preview`, `gemini-2.5-flash-lite-preview-tts`.
