package edgar

import (
	"bufio"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

type secFilerInfo struct {
	Info struct {
		CIK     string `xml:"cik"`
		SIC     string `xml:"assigned-sic,omitempty"`
		SICDesc string `xml:"assigned-sic-desc,omitempty"`
		Name    string `xml:"conformed-name"`
	} `xml:"company-info"`
}

func parseCikAndDocID(url string) (string, string) {
	var s1 string
	var d1, d2, d3, d4 int
	fmt.Sscanf(url, "/cgi-bin/viewer?action=view&cik=%d&accession_number=%d-%d-%d%s", &d1, &d2, &d3, &d4, &s1)
	cik := fmt.Sprintf("%d", d1)
	an := fmt.Sprintf("%010d%d%d", d2, d3, d4)
	return cik, an
}

/*
  This is the parsing of query page where we get the list of filings of a given types
  ex: https://www.sec.gov/cgi-bin/browse-edgar?CIK=AAPL&owner=exclude&action=getcompany&type=10-Q&count=1&dateb=
  Assumptions of the parser:
  - There is interactive data available and there is a button that allows the user to click it
  - Since it is a link the tag will be a hyperlink with a button with the id=interactiveDataBtn
  - The actual link is the href attribute in the "a" token just before the id attribute
*/
func queryPageParser(page io.Reader, docType FilingType) map[string]string {

	filingInfo := make(map[string]string)

	z := html.NewTokenizer(page)

	data, err := parseTableRow(z, true)
	for err == nil {
		//This check for filing type will drop AMEND filings
		if len(data) == 5 && data[0] == string(docType) {
			//Drop filings before 2010
			year := getYear(data[3])
			if year >= thresholdYear {
				filingInfo[data[3]] = data[1]
			}
		}
		data, err = parseTableRow(z, true)
	}
	return filingInfo
}

func cikPageParser(page io.Reader) (string, error) {
	var feed secFilerInfo

	decoder := xml.NewDecoder(page)
	decoder.CharsetReader = charset.NewReaderLabel
	err := decoder.Decode(&feed)
	if err != nil {
		return "", errors.New(fmt.Sprintf("Could not find the CIK: %v", err))
	}

	return feed.Info.CIK, nil
}

/*
  The filing page parser
  - The top of the page has a list of reports.
  - Get all the reports (link to all the reports) and put it in an array
  - The Accordian on the side of the page identifies what each report is
  - Get the text of the accordian and map the type of the report to the report
  - Create a map of the report to report link
*/
func filingPageParser(page io.Reader, fileType FilingType) map[filingDocType]string {
	var filingLinks []string
	r := bufio.NewReader(page)
	s, e := r.ReadString('\n')

	for e == nil {
		//Get the number of reports available
		if strings.Contains(s, "var reports") == true {
			s1 := strings.Split(s, "(")
			s2 := strings.Split(s1[1], ")")
			cnt, _ := strconv.Atoi(s2[0])

			//cnt-1 because we skip the 'all' in the list
			for i := 0; i < cnt-1; i++ {
				if s, e = r.ReadString('\n'); e != nil {
					panic(e.Error())
				}
				s1 := strings.Split(s, " = ")
				s2 := strings.Split(s1[1], ";")
				s3 := strings.Trim(s2[0], "\"")
				s4 := strings.Split(s3, ".")
				s5 := s3
				//Sometimes the report is listed as an xml file??
				if s4[1] == "xml" {
					s5 = s4[0] + ".htm"
				}
				if !strings.Contains(s5, "htm") {
					panic("Dont know this type of report")
				}
				filingLinks = append(filingLinks, s5)
			}

			break
		}
		s, e = r.ReadString('\n')

	}

	docs := mapReports(page, filingLinks)
	return docs

}

func parseTableData(z *html.Tokenizer, parseHref bool) string {
	token := z.Token()

	if token.Type != html.StartTagToken && token.Data != "td" {
		log.Fatal("Tokenizer passed incorrectly to parseTableData")
		return ""
	}

	for !(token.Data == "td" && token.Type == html.EndTagToken) {
		if token.Type == html.ErrorToken {
			break
		}

		if parseHref && token.Data == "a" && token.Type == html.StartTagToken {
			str := parseHyperLinkTag(z, token)
			if len(str) > 0 {
				return str
			}
		} else {
			if token.Type == html.TextToken {
				str := strings.TrimSpace(token.String())
				if len(str) > 0 {
					return str
				}
			}
		}
		//Going for the end of the td tag
		z.Next()
		token = z.Token()
	}
	return ""
}

func parseTableRow(z *html.Tokenizer, parseHref bool) ([]string, error) {
	var retData []string
	//Get the current token
	token := z.Token()

	//Check if this is really a table row
	for !(token.Type == html.StartTagToken && token.Data == "tr") {
		tt := z.Next()
		if tt == html.ErrorToken {
			return nil, errors.New("Done with parsing")
		}
		token = z.Token()
	}
	//Till the end of the row collect data from each data block
	for !(token.Data == "tr" && token.Type == html.EndTagToken) {

		if token.Type == html.ErrorToken {
			return nil, errors.New("Done with parsing")
		}
		if token.Data == "td" && token.Type == html.StartTagToken {
			parseFlag := parseHref
			//If the data is a number class just get the text = number
			for _, a := range token.Attr {
				if a.Key == "class" && (a.Val == "nump" || a.Val == "num") {
					parseFlag = false
				}
			}
			str := parseTableData(z, parseFlag)
			if len(str) > 0 {
				retData = append(retData, str)
			}
		}
		z.Next()
		token = z.Token()
	}

	return retData, nil
}

var reqHyperLinks = map[string]bool{
	"interactiveDataBtn": true,
}

func parseHyperLinkTag(z *html.Tokenizer, token html.Token) string {
	var href string
	var onclick string
	var id string

	for _, a := range token.Attr {
		switch a.Key {
		case "id":
			id = a.Val
		case "href":
			href = a.Val
		case "onclick":
			onclick = a.Val
			if str, err := getFinDataXBRLTag(onclick); err == nil {
				return str
			}
		}
	}

	text := ""
	//Finish up the hyperlink
	for !(token.Data == "a" && token.Type == html.EndTagToken) {
		/*
			if token.Type == html.TextToken {
				str := strings.TrimSpace(token.String())
				if len(str) > 0 {
					text = str
				}
			}
		*/
		z.Next()
		token = z.Token()
	}

	if _, ok := reqHyperLinks[id]; ok {
		return href
	}

	return text
}

func parseTableTitle(z *html.Tokenizer) []string {

	var strs []string
	token := z.Token()

	if token.Type != html.StartTagToken && token.Data != "th" {
		log.Fatal("Tokenizer passed incorrectly to parseTableData")
		return strs
	}

	for !(token.Data == "th" && token.Type == html.EndTagToken) {
		if token.Type == html.ErrorToken {
			break
		}

		if token.Type == html.TextToken {
			str := strings.TrimSpace(token.String())
			if len(str) > 0 {
				strs = append(strs, str)
			}
		}
		//Going for the end of the td tag
		z.Next()
		token = z.Token()
	}
	return strs
}

func parseTableHeading(z *html.Tokenizer) ([]string, error) {
	var retData []string
	//Get the current token
	token := z.Token()

	//Check if this is really a table row
	for !(token.Type == html.StartTagToken && token.Data == "tr") {
		tt := z.Next()
		if tt == html.ErrorToken {
			return nil, errors.New("Done with parsing")
		}
		token = z.Token()
	}

	//Till the end of the row collect data from each data block
	for !(token.Data == "tr" && token.Type == html.EndTagToken) {

		if token.Type == html.ErrorToken {
			return nil, errors.New("Done with parsing")
		}
		if token.Data == "th" && token.Type == html.StartTagToken {
			str := parseTableTitle(z)
			if len(str) > 0 {
				retData = append(retData, str...)
			}
		}
		z.Next()
		token = z.Token()
	}

	return retData, nil
}

func parseFilingScale(z *html.Tokenizer, t filingDocType) map[scaleEntity]scaleFactor {
	scales := make(map[scaleEntity]scaleFactor)
	data, err := parseTableHeading(z)
	if err == nil {
		if len(data) > 0 {
			scales = filingScale(data, t)
		}
	}
	return scales
}

/*
	This function takes any report filed under a company and looks
	for XBRL tags that Filing is interested in to gather and store.
	XBRL tag is mapped to a finDataType which is then used to lookup
	the passed in interface fields to see if there is a match and set
	that field
*/

func finReportParser(page io.Reader, fr *financialReport, t filingDocType) (*financialReport, error) {

	z := html.NewTokenizer(page)
	scales := parseFilingScale(z, t)
	data, err := parseTableRow(z, true)
	for err == nil {
		if len(data) > 0 {
			finType := getFinDataTypeFromXBRLTag(data[0])
			if finType != finDataUnknown {
				for _, str := range data[1:] {
					if len(str) > 0 {
						if setData(fr, finType, str, scales, t) == nil {
							break
						}
					}
				}
			}
		}
		data, err = parseTableRow(z, true)
	}
	fr.Scales = scales
	return fr, nil
}

// parseAllReports gets all the reports filed under a given account normalizeNumber
func parseAllReports(cik string, an string) []int {

	var reports []int
	url := "https://www.sec.gov/Archives/edgar/data/" + cik + "/" + an + "/"
	page := getPage(url)
	z := html.NewTokenizer(page)
	data, err := parseTableRow(z, false)
	for err == nil {
		var num int
		if len(data) > 0 && strings.Contains(data[0], "R") {
			_, err = fmt.Sscanf(data[0], "R%d.htm", &num)
			if err == nil {
				reports = append(reports, num)
			}
		}
		data, err = parseTableRow(z, false)
	}
	sort.Slice(reports, func(i, j int) bool {
		return reports[i] < reports[j]
	})
	return reports
}

func parseMappedReports(docs map[filingDocType]string, docType FilingType) (*financialReport, error) {
	var wg sync.WaitGroup
	fr := newFinancialReport(docType)
	for t, url := range docs {
		wg.Add(1)
		go func(url string, fr *financialReport, t filingDocType) {
			defer wg.Done()
			page := getPage(url)
			if page != nil {
				finReportParser(page, fr, t)
			}
		}(baseURL+url, fr, t)
	}
	wg.Wait()
	return fr, validateFinancialReport(fr)
}
