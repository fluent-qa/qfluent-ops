package proxy

import (
	"bufio"
	"github.com/fluent-qa/qfluent-ops/config"
	"io"
	"log"
	"net/http"
	"sync"
	"unicode/utf8"
)

var keyIndex sync.Map

func getClientIP(r *http.Request) string {
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.Header.Get("X-Real-Ip")
	}
	if ip == "" {
		ip = r.RemoteAddr
	}
	return ip
}

func writeCharByChar(w http.ResponseWriter, r io.Reader) {
	reader := bufio.NewReader(r)
	for {
		char, _, err := reader.ReadRune()
		if err != nil {
			if err != io.EOF {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			break
		}

		buf := make([]byte, utf8.RuneLen(char))
		utf8.EncodeRune(buf, char)

		w.Write(buf)
		w.(http.Flusher).Flush()
	}
}

func ProxyHandler(config config.OpenAIConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		if len(authorization) == 0 {
			errorMessage := "Authorization header is missing"
			log.Printf("[Error] %s", errorMessage)
			http.Error(w, errorMessage, http.StatusBadRequest)
			return
		}

		if len(authorization) < 9 || authorization[:7] != "Bearer " {
			errorMessage := "Invalid Authorization header format"
			log.Printf("[Error] %s", errorMessage)
			http.Error(w, errorMessage, http.StatusBadRequest)
			return
		}

		token := authorization[7:]
		log.Printf("Parsed token: %s", token)

		if token[:3] == "genai-" {
			keys, ok := config.Keys[token]
			if !ok {
				errorMessage := `{"error":{"message":"Invalid Token","code":403}}`
				log.Printf("[Error] %s", errorMessage)
				http.Error(w, errorMessage, http.StatusForbidden)
				return
			}

			index, _ := keyIndex.LoadOrStore(token, 0)
			r.Header.Set("Authorization", "Bearer "+keys[index.(int)])

			nextIndex := (index.(int) + 1) % len(keys)
			keyIndex.Store(token, nextIndex)
			log.Printf("Used key: %s, Updated index: %d", keys[index.(int)], nextIndex)
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}

		var baseURL string
		if config.BaseURL != "" {
			baseURL = config.BaseURL
		} else {
			if config.Helicone != "" {
				baseURL = "https://oai.hconeai.com"
			} else {
				baseURL = "https://api.openai.com"
			}
		}
		log.Printf("Using baseURL: %s", baseURL)

		r.Header.Del("CF-Connecting-IP")
		r.Header.Del("X-Forwarded-For")
		r.Header.Del("X-Real-IP")
		r.Header.Del("X-Envoy-External-Address")
		r.Header.Del("X-Forwarded-Host")
		r.Header.Del("X-Forwarded-Proto")
		r.Header.Del("Cf-Ray")
		r.Header.Del("Cf-Visitor")
		r.Header.Del("Cf-Ipcountry")
		r.Header.Del("Cf-Request-Id")
		r.Host = baseURL

		proxyURL := baseURL + r.RequestURI
		req, err := http.NewRequest(r.Method, proxyURL, r.Body)
		if err != nil {
			errorMessage := "Error creating proxy request"
			log.Printf("[Error] %s: %v", errorMessage, err)
			http.Error(w, errorMessage, http.StatusInternalServerError)
			return
		}

		req.URL.RawQuery = r.URL.RawQuery

		req.Header = r.Header
		req.Header.Set("Transfer-Encoding", r.Header.Get("Transfer-Encoding"))
		req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
		if config.Helicone != "" {
			clientIP := getClientIP(r)
			req.Header.Set("Helicone-Auth", "Bearer "+config.Helicone)
			req.Header.Set("helicone-user-id", clientIP)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errorMessage := "Error sending proxy request"
			log.Printf("[Error] %s: %v", errorMessage, err)
			http.Error(w, errorMessage, http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.Header().Set("Transfer-Encoding", resp.Header.Get("Transfer-Encoding"))
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)

		writeCharByChar(w, resp.Body)
	}
}

// TODO: Load API COnfig
func RunProxyServer(config config.OpenAIConfig) {
	config, err := config("config/config.json")
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}
	http.HandleFunc("/", ProxyHandler(config))
	log.Fatal(http.ListenAndServe(":8124", nil))
}
