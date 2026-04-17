package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
)

type Transaction struct {
	Symbol string
	Price  float64
	Qty    float64
	Date   time.Time
}

var (
	dateRe  = regexp.MustCompile(`^\d{2}-[A-Za-z]{3}-\d{4}$`)
	alnumRe = regexp.MustCompile(`^[A-Z0-9]+$`)
)

func main() {
	pdfPath := flag.String("pdf", "", "path to CAS PDF file")
	password := flag.String("password", "", "PDF password")
	flag.Parse()

	if *pdfPath == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "usage: go run . -pdf /path/to/cas.pdf -password 'Linux11!'")
		os.Exit(2)
	}

	txs, err := ParseCASTransactions(*pdfPath, *password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse failed: %v\n", err)
		os.Exit(1)
	}

	for _, tx := range txs {
		fmt.Printf("- %s, %s, %s, %s\n",
			tx.Symbol,
			formatFloat(tx.Price, 3, 4),
			formatFloat(tx.Qty, 3, 3),
			strings.ToLower(tx.Date.Format("02-Jan-2006")),
		)
	}
}

func ParseCASTransactions(pdfPath, password string) ([]Transaction, error) {
	text, err := extractPlainText(pdfPath, password)
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

func extractPlainText(pdfPath, password string) (string, error) {
	f, err := os.Open(pdfPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return "", err
	}

	pwProvided := false
	reader, err := pdf.NewReaderEncrypted(f, st.Size(), func() string {
		if pwProvided {
			return ""
		}
		pwProvided = true
		return password
	})
	if err != nil {
		return "", err
	}

	plainReader, err := reader.GetPlainText()
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

func cleanLine(s string) string {
	return strings.TrimSpace(s)
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

func formatFloat(v float64, minDecimals, maxDecimals int) string {
	s := strconv.FormatFloat(v, 'f', maxDecimals, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")

	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		if minDecimals > 0 {
			return s + "." + strings.Repeat("0", minDecimals)
		}
		return s
	}

	decimals := len(s) - dot - 1
	if decimals < minDecimals {
		s += strings.Repeat("0", minDecimals-decimals)
	}
	return s
}
