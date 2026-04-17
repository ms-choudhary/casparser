package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
)

type Transaction struct {
	Symbol string    `json:"symbol"`
	Price  float64   `json:"price"`
	Qty    float64   `json:"qty"`
	Date   time.Time `json:"date"`
}

type parseResponse struct {
	Transactions []Transaction `json:"transactions"`
}

type errorResponse struct {
	Error string `json:"error"`
}

const maxPDFUploadSize = 20 << 20 // 20 MB

var (
	dateRe  = regexp.MustCompile(`^\d{2}-[A-Za-z]{3}-\d{4}$`)
	alnumRe = regexp.MustCompile(`^[A-Z0-9]+$`)
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/api/parse-cas", parseCASHandler)

	addr := ":" + envOrDefault("PORT", "8080")
	fmt.Printf("listening on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "server failed: %v\n", err)
		os.Exit(1)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func parseCASHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPDFUploadSize)
	if err := r.ParseMultipartForm(maxPDFUploadSize); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid multipart form or file too large"})
		return
	}

	password := strings.TrimSpace(r.FormValue("password"))
	if password == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing required field: password"})
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing required file field: file"})
		return
	}
	defer file.Close()

	pdfData, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "failed to read uploaded file"})
		return
	}
	if len(pdfData) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "uploaded file is empty"})
		return
	}

	transactions, err := ParseCASTransactions(pdfData, password)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, parseResponse{Transactions: transactions})
}

func ParseCASTransactions(pdfBytes []byte, password string) ([]Transaction, error) {
	text, err := extractPlainText(pdfBytes, password)
	if err != nil {
		return nil, err
	}

	lines := splitLines(text)
	txs := make([]Transaction, 0, 8)
	currentISIN := ""

	for i := 0; i < len(lines); i++ {
		line := cleanLine(lines[i])
		if line == "" {
			continue
		}

		if strings.EqualFold(line, "ISIN") {
			isin, next := parseISIN(lines, i+1)
			if isin != "" {
				currentISIN = isin
				i = next - 1
			}
			continue
		}

		if currentISIN == "" || !dateRe.MatchString(line) {
			continue
		}
		if i+4 >= len(lines) {
			continue
		}

		amount, okAmount := parseNumber(lines[i+1])
		price, okPrice := parseNumber(lines[i+2])
		qty, okQty := parseNumber(lines[i+3])
		desc := cleanLine(lines[i+4])
		if !okAmount || !okPrice || !okQty || desc == "" {
			continue
		}

		descLower := strings.ToLower(desc)
		if strings.Contains(descLower, "stamp duty") {
			continue
		}
		if !strings.Contains(descLower, "purchase") && !strings.Contains(descLower, "investment") {
			continue
		}

		date, err := time.Parse("02-Jan-2006", line)
		if err != nil {
			continue
		}

		_ = amount // kept for optional reconciliation/debug.
		txs = append(txs, Transaction{
			Symbol: currentISIN,
			Price:  price,
			Qty:    qty,
			Date:   date,
		})

		i += 4
	}

	if len(txs) == 0 {
		return nil, errors.New("no transactions parsed")
	}
	return txs, nil
}

func extractPlainText(pdfBytes []byte, password string) (string, error) {
	reader := bytes.NewReader(pdfBytes)
	pwProvided := false
	pdfReader, err := pdf.NewReaderEncrypted(reader, int64(len(pdfBytes)), func() string {
		if pwProvided {
			return ""
		}
		pwProvided = true
		return password
	})
	if err != nil {
		return "", err
	}

	plainReader, err := pdfReader.GetPlainText()
	if err != nil {
		return "", err
	}

	data, err := io.ReadAll(plainReader)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

func parseISIN(lines []string, start int) (string, int) {
	var b strings.Builder
	started := false

	for i := start; i < len(lines) && i < start+32; i++ {
		token := strings.ToUpper(cleanLine(lines[i]))
		if token == "" || token == ":" || token == "-" {
			continue
		}
		if strings.HasPrefix(token, "ADVISOR") || strings.HasPrefix(token, "REGISTRAR") || strings.HasPrefix(token, "FOLIO") {
			break
		}
		if !alnumRe.MatchString(token) {
			continue
		}

		if !started {
			if !strings.HasPrefix(token, "INF") {
				continue
			}
			started = true
		}

		b.WriteString(token)
		if b.Len() >= 12 {
			s := b.String()
			if len(s) > 12 {
				s = s[:12]
			}
			return s, i + 1
		}
	}

	return "", start
}

func parseNumber(s string) (float64, bool) {
	s = cleanLine(s)
	if s == "" {
		return 0, false
	}
	clean := strings.ReplaceAll(s, ",", "")
	v, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

func cleanLine(s string) string {
	return strings.TrimSpace(s)
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
