package billing

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	"axiom/internal/platform/egress"
)

const exchangeURL = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"

// ECB quotes are decimal text against EUR. Cross rates divide these exact values, never rounded intermediates.
func fetchExchange(ctx context.Context, client *egress.Client, url string, at time.Time) (inputs []ExchangeInput, date time.Time, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("billing: exchange source status %d", resp.StatusCode)
	}
	raw, err := egress.ReadBody(resp.Body, 64<<10)
	if err != nil {
		return nil, time.Time{}, err
	}
	return parseExchange(raw, at)
}

func parseExchange(raw []byte, at time.Time) ([]ExchangeInput, time.Time, error) {
	var doc struct {
		XMLName xml.Name `xml:"http://www.gesmes.org/xml/2002-08-01 Envelope"`
		Days    []struct {
			Date  string          `xml:"time,attr"`
			Rates []exchangeQuote `xml:"Cube"`
		} `xml:"Cube>Cube"`
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&doc); err != nil {
		return nil, time.Time{}, fmt.Errorf("billing: exchange XML: %w", err)
	}
	if err := exchangeXMLTail(dec); err != nil {
		return nil, time.Time{}, err
	}
	if len(doc.Days) != 1 {
		return nil, time.Time{}, errors.New("billing: exchange source needs one date")
	}
	day := doc.Days[0]
	date, err := time.Parse(time.DateOnly, day.Date)
	if err != nil || date.After(at.UTC()) {
		return nil, time.Time{}, errors.New("billing: invalid exchange source date")
	}
	quotes, err := exchangeQuotes(day.Rates)
	if err != nil {
		return nil, time.Time{}, err
	}
	inputs, err := crossExchangeQuotes(quotes, day.Date)
	return inputs, date, err
}

type exchangeQuote struct {
	Currency string `xml:"currency,attr"`
	Rate     string `xml:"rate,attr"`
}

func exchangeQuotes(rows []exchangeQuote) (map[string]*big.Rat, error) {
	quotes := map[string]*big.Rat{"EUR": big.NewRat(1, 1)}
	for _, row := range rows {
		if quotes[row.Currency] != nil {
			return nil, errors.New("billing: duplicate exchange source currency")
		}
		if len(row.Rate) == 0 || len(row.Rate) > 32 || strings.Trim(row.Rate, "0123456789.") != "" || strings.Count(row.Rate, ".") > 1 {
			return nil, errors.New("billing: invalid exchange source decimal")
		}
		value, ok := new(big.Rat).SetString(row.Rate)
		if !ok || value.Sign() <= 0 {
			return nil, errors.New("billing: nonpositive exchange source rate")
		}
		quotes[row.Currency] = value
	}
	return quotes, nil
}

func crossExchangeQuotes(quotes map[string]*big.Rat, day string) ([]ExchangeInput, error) {
	codes := slices.Sorted(maps.Keys(exchangeCurrencies))
	for _, code := range codes {
		if quotes[code] == nil {
			return nil, errors.New("billing: incomplete exchange source currencies")
		}
	}
	var inputs []ExchangeInput
	for i, base := range codes {
		for _, quote := range codes[i+1:] {
			value := new(big.Rat).Quo(quotes[quote], quotes[base])
			in := ExchangeInput{Base: base, Quote: quote, Numerator: value.Num().String(), Denominator: value.Denom().String(), Kind: "initial", Reason: "ECB reference rates " + day}
			if _, err := in.normalized(); err != nil {
				return nil, err
			}
			inputs = append(inputs, in)
		}
	}
	return inputs, nil
}

func exchangeXMLTail(dec *xml.Decoder) error {
	for {
		token, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if text, ok := token.(xml.CharData); !ok || strings.TrimSpace(string(text)) != "" {
			return errors.New("billing: trailing exchange XML")
		}
	}
}
