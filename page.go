package edgar

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"time"
)

var (
	baseURL   = "https://www.sec.gov/"
	cikURL    = "https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&output=xml&CIK=%s"
	queryURL  = "cgi-bin/browse-edgar?action=getcompany&CIK=%s&type=%s&dateb=&owner=exclude&count=10"
	searchURL = baseURL + queryURL
)

func createQueryURL(symbol string, docType FilingType) string {
	return fmt.Sprintf(searchURL, symbol, docType)
}

func getPage(url string) io.ReadCloser {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Fatalf("Query to SEC page %s failed: %v", url, err)
		return nil
	}
	agent := fmt.Sprintf("edgar/%s", runtime.Version())
	req.Header.Set("User-Agent", agent)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Query to SEC page %s failed: %v", url, err)
		return nil
	}
	return resp.Body
}

func getCompanyCIK(ticker string) string {
	url := fmt.Sprintf(cikURL, ticker)
	r := getPage(url)
	if r != nil {
		defer r.Close()
		if cik, err := cikPageParser(r); err == nil {
			return cik
		}
	}
	return ""
}

// getFilingLinks gets the links for filings of a given type of filing 10K/10Q..
func getFilingLinks(ticker string, fileType FilingType) map[string]string {
	url := createQueryURL(ticker, fileType)
	resp := getPage(url)
	if resp == nil {
		log.Println("No response on the query for docs")
		return nil
	}
	defer resp.Close()
	return queryPageParser(resp, fileType)

}

//Get all the docs pages based on the filing type
//Returns a map:
// key=Document type ex.Cash flow statement
// Value = link to that that sheet
func getFilingDocs(url string, fileType FilingType) map[filingDocType]string {
	url = baseURL + url
	resp := getPage(url)
	if resp == nil {
		return nil
	}
	defer resp.Close()
	return filingPageParser(resp, fileType)
}

// getFinancialData gets the data from all the filing docs and places it in
// a financial report
func getFinancialData(url string, fileType FilingType) (*financialReport, error) {
	docs := getFilingDocs(url, fileType)
	return parseMappedReports(docs, fileType)
}
