package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	sourcePDF = "input/source.pdf"
	sourceDir = "input"
)

var pageTitles = map[int]string{
	2:  "nakupovani_a_jidlo",
	3:  "domacnost_a_sousedi",
	4:  "opravy_a_sluzby",
	5:  "doprava_a_orientace",
	6:  "lekar_a_zdravi",
	7:  "urady_a_administrativa",
	8:  "konverzace_a_spolecnost",
	9:  "prazdna_stranka",
	10: "restaurace_a_jidlo_venku",
	11: "bydleni_a_domacnost_pokracovani",
	12: "uklid_a_prani",
	13: "technologie_a_komunikace",
	14: "nakupovani_online_a_zasilky",
	15: "uceni_a_prace",
	16: "cas_a_datum",
	17: "pocasi_a_priroda",
	18: "volny_cas_a_sport",
	19: "jidlo_a_piti_pokracovani",
	20: "cestovani_a_ubytovani",
	21: "zdravotni_situace_pokracovani",
	22: "finance_a_penize",
	23: "bezpecnost_a_nouzove_situace",
	24: "spolecensky_zivot_a_zabava",
	25: "zeme_a_cestovani",
	26: "zakladni_prislovce_a_spojky",
	27: "lide_a_vztahy",
}

type vocabPair struct {
	Spanish string
	Czech   string
}

var whitespace = regexp.MustCompile(`\s+`)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return errors.New("pdftotext was not found in PATH")
	}
	if _, err := os.Stat(sourcePDF); err != nil {
		return fmt.Errorf("cannot read %s: %w", sourcePDF, err)
	}

	created := 0
	for page := 2; page <= 27; page++ {
		pairs, err := extractPairs(page)
		if err != nil {
			return err
		}
		if err := writePageFiles(page, pairs); err != nil {
			return err
		}
		created += 2
		fmt.Printf("%02d page=%02d pairs=%02d %s\n", page-1, page, len(pairs), pageTitles[page])
	}

	fmt.Printf("created %d files\n", created)
	return nil
}

func extractPairs(page int) ([]vocabPair, error) {
	text, err := extractPageText(page)
	if err != nil {
		return nil, err
	}

	var pairs []vocabPair
	for _, rawLine := range strings.Split(text, "\n") {
		line := cleanLine(rawLine)
		if line == "" || isPageNumber(line) || !strings.Contains(line, "–") {
			continue
		}

		spanish, czech, ok := strings.Cut(line, "–")
		if !ok {
			continue
		}
		spanish = strings.TrimSpace(spanish)
		czech = strings.TrimSpace(czech)
		if spanish == "" || czech == "" {
			continue
		}

		pairs = append(pairs, vocabPair{Spanish: spanish, Czech: czech})
	}
	return pairs, nil
}

func extractPageText(page int) (string, error) {
	cmd := exec.Command("pdftotext", "-f", fmt.Sprint(page), "-l", fmt.Sprint(page), "-layout", sourcePDF, "-")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("extract page %d: %w: %s", page, err, strings.TrimSpace(stderr.String()))
	}
	return string(output), nil
}

func writePageFiles(page int, pairs []vocabPair) error {
	title, ok := pageTitles[page]
	if !ok {
		return fmt.Errorf("missing title for page %d", page)
	}

	stem := fmt.Sprintf("%02d-%s", page-1, title)
	spanishPath := filepath.Join(sourceDir, stem+"_es.txt")
	czechPath := filepath.Join(sourceDir, stem+"_cz.txt")

	spanish := make([]string, 0, len(pairs))
	czech := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		spanish = append(spanish, pair.Spanish)
		czech = append(czech, pair.Czech)
	}

	if err := writeLines(spanishPath, spanish); err != nil {
		return err
	}
	if err := writeLines(czechPath, czech); err != nil {
		return err
	}
	return nil
}

func writeLines(path string, lines []string) error {
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func cleanLine(line string) string {
	return whitespace.ReplaceAllString(strings.TrimSpace(line), " ")
}

func isPageNumber(line string) bool {
	for _, char := range line {
		if char < '0' || char > '9' {
			return false
		}
	}
	return line != ""
}
