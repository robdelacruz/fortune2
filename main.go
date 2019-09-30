package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type FortuneFmt int

const (
	PlainText = FortuneFmt(iota)
	HtmlPre
	Html
	Json
)

func main() {
	var db *sql.DB
	var err error
	var cmd string
	var fortunefile string

	os.Args = os.Args[1:]

	switches, parms := parseArgs(os.Args)

	// fortune db file is read from the following, in order of priority:
	// 1. -F fortune_file switch
	// 2. $FORTUNE2FILE env var
	// 3. /usr/local/share/fortune2/fortune2.db (default)
	fortunefile = os.Getenv("FORTUNE2FILE")
	if switches["F"] != "" {
		fortunefile = switches["F"]
	}
	if fortunefile == "" {
		dirpath := filepath.Join(string(os.PathSeparator), "usr", "local", "share", "fortune2")
		os.MkdirAll(dirpath, os.ModePerm)
		fortunefile = filepath.Join(dirpath, "fortune2.db")
	}
	db, err = sql.Open("sqlite3", fortunefile)
	if err != nil {
		log.Fatal(err)
	}

	cmd = "random"
	if len(parms) > 0 {
		if parms[0] == "ingest" || parms[0] == "delete" || parms[0] == "search" || parms[0] == "info" || parms[0] == "random" || parms[0] == "serve" {
			cmd = parms[0]
			parms = parms[1:]
		}
	}

	switch cmd {
	case "info":
		fmt.Printf("fortune db file:  %s\n\n", fortunefile)
		tbls := allTables(db)
		if len(tbls) == 0 {
			fmt.Println("No fortune jars yet.\n Use 'ingest' to initialize one.")
			os.Exit(1)
		}

		printJarStats(db, tbls)
	case "ingest":
		for _, jarfile := range parms {
			ingestJarFile(db, jarfile)
		}
	case "delete":
		for _, jar := range parms {
			deleteJar(db, jar)
		}
	case "search":
		var q string
		var jars []string

		if len(parms) > 0 {
			q = parms[0]
			parms = parms[1:]
			jars = parms
		}
		if len(jars) == 0 {
			jars = allTables(db)
		}
		for _, jar := range jars {
			allFortunes(db, jar, q, switches, os.Stdout)
		}
	case "random":
		if len(allTables(db)) == 0 {
			fmt.Println("No fortune jars yet.\n Use 'ingest' to initialize one.")
			os.Exit(1)
		}

		if switches["f"] != "" {
			printJarStats(db, parms)
			break
		}
		fortune, jar := randomFortune(db, parms, switches)
		if switches["c"] != "" {
			fmt.Printf("(%s)\n", jar)
		}
		fmt.Println(fortune)
	case "serve":
		port := "8000"
		if len(parms) > 0 {
			port = parms[0]
		}
		http.Handle("/asset/", http.StripPrefix("/asset/", http.FileServer(http.Dir("./asset"))))
		http.HandleFunc("/fortune/", fortuneHandler(db))
		http.HandleFunc("/site/", siteHandler(db))
		http.HandleFunc("/", rootHandler(db))

		fmt.Printf("Listening on %s...\n", port)
		err = http.ListenAndServe(fmt.Sprintf(":%s", port), nil)
		log.Fatal(err)
	}
}

func listContains(ss []string, v string) bool {
	for _, s := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func parseArgs(args []string) (map[string]string, []string) {
	switches := map[string]string{}
	parms := []string{}

	standaloneSwitches := []string{"c", "e", "f", "i"}
	definitionSwitches := []string{"F"}
	fNoMoreSwitches := false
	curKey := ""

	for _, arg := range args {
		if fNoMoreSwitches {
			// any arg after "--" is a standalone parameter
			parms = append(parms, arg)
		} else if arg == "--" {
			// "--" means no more switches to come
			fNoMoreSwitches = true
		} else if strings.HasPrefix(arg, "--") {
			switches[arg[2:]] = "y"
			curKey = ""
		} else if strings.HasPrefix(arg, "-") {
			if listContains(definitionSwitches, arg[1:]) {
				// -a "val"
				curKey = arg[1:]
				continue
			}
			for _, ch := range arg[1:] {
				// -a, -b, -ab
				sch := string(ch)
				if listContains(standaloneSwitches, sch) {
					switches[sch] = "y"
				}
			}
		} else if curKey != "" {
			switches[curKey] = arg
			curKey = ""
		} else {
			// standalone parameter
			parms = append(parms, arg)
		}
	}

	return switches, parms
}

func printJarStats(db *sql.DB, jars []string) {
	if len(jars) == 0 {
		jars = allTables(db)
	}

	jarNumRows := map[string]int{}
	var totalRows int
	for _, jar := range jars {
		nRows := queryNumRows(db, jar)
		jarNumRows[jar] = nRows
		totalRows += nRows
	}

	fmt.Printf("%-20s  %8s  %6s\n", "Fortune Jar", "fortunes", "%")
	fmt.Printf("%-20s  %8s  %6s\n", strings.Repeat("-", 20), strings.Repeat("-", 8), strings.Repeat("-", 6))
	for _, jar := range jars {
		nRows := jarNumRows[jar]
		pctTotal := 0.0
		if totalRows > 0 {
			pctTotal = float64(nRows) / float64(totalRows) * 100
		}
		fmt.Printf("%-20s  %8d  %6.2f\n", jar, nRows, pctTotal)
	}
}

func ingestJarFile(db *sql.DB, jarfile string) {
	var sb strings.Builder
	var body string

	f, err := os.Open(jarfile)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	jar := strings.Split(filepath.Base(jarfile), ".")[0]
	fmt.Printf("Writing '%s' to table '%s'...", jarfile, jar)

	_, err = db.Exec("BEGIN TRANSACTION")
	if err != nil {
		log.Fatal(err)
	}

	sqlstr := fmt.Sprintf("DROP TABLE IF EXISTS [%s]", jar)
	_, err = db.Exec(sqlstr)
	if err != nil {
		log.Fatal(err)
	}
	sqlstr = fmt.Sprintf("CREATE TABLE [%s] (id INTEGER PRIMARY KEY NOT NULL, body TEXT)", jar)
	_, err = db.Exec(sqlstr)
	if err != nil {
		log.Fatal(err)
	}

	sqlstr = fmt.Sprintf("INSERT INTO [%s] (body) VALUES (?)", jar)
	insertStmt, err := db.Prepare(sqlstr)
	if err != nil {
		log.Fatal(err)
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "%" {
			body = sb.String()
			sb.Reset()
			if len(strings.TrimSpace(body)) == 0 {
				continue
			}
			_, err = insertStmt.Exec(body)
			if err != nil {
				log.Fatal(err)
			}
			continue
		}

		sb.WriteString(line)
		sb.WriteString("\n")
	}
	body = sb.String()
	if len(strings.TrimSpace(body)) > 0 {
		_, err = insertStmt.Exec(body)
		if err != nil {
			log.Fatal(err)
		}
	}

	_, err = db.Exec("COMMIT")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Done.\n")
}

func deleteJar(db *sql.DB, jar string) {
	var err error
	var sqlstr string

	_, err = db.Exec("BEGIN TRANSACTION")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Deleting jar '%s'...", jar)
	sqlstr = fmt.Sprintf("DROP TABLE IF EXISTS [%s]", jar)
	_, err = db.Exec(sqlstr)

	_, err = db.Exec("COMMIT")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Done.\n")
}

func randomJar(db *sql.DB, jars []string) string {
	rand.Seed(time.Now().UnixNano())

	if len(jars) == 0 {
		jars = allTables(db)
	}

	return jars[rand.Intn(len(jars))]
}

func randomJarByWeight(db *sql.DB, jars []string) string {
	rand.Seed(time.Now().UnixNano())

	if len(jars) == 0 {
		jars = allTables(db)
	}

	jarNumRows := map[string]int{}
	var totalRows int
	for _, jar := range jars {
		nRows := queryNumRows(db, jar)
		jarNumRows[jar] = nRows
		totalRows += nRows
	}

	// None of the jars exist, so just select from all jars.
	if totalRows == 0 {
		return randomJar(db, allTables(db))
	}

	npick := rand.Intn(totalRows)

	var pickJar string
	var sumRows int
	for _, jar := range jars {
		sumRows += jarNumRows[jar]
		if npick < sumRows {
			pickJar = jar
			break
		}
	}
	if pickJar == "" {
		log.Fatalf("No jar was picked. totalRows=%d npick=%d", totalRows, npick)
	}
	return pickJar
}

func randomJarFortune(db *sql.DB, jar string) string {
	var body string
	var err error

	sqlstr := fmt.Sprintf("SELECT body FROM [%s] WHERE rowid = (abs(random()) %% (SELECT max(rowid) FROM [%s]) + 1)", jar, jar)
	row := db.QueryRow(sqlstr)
	err = row.Scan(&body)
	if err == sql.ErrNoRows {
		return ""
	}
	return body
}

func jarFortune(db *sql.DB, jar string, jarIndex string) string {
	var body string
	var err error

	sqlstr := fmt.Sprintf("SELECT body FROM [%s] WHERE rowid = %s", jar, jarIndex)
	row := db.QueryRow(sqlstr)
	err = row.Scan(&body)
	if err == sql.ErrNoRows {
		return ""
	}
	return body
}

func randomFortune(db *sql.DB, jars []string, options map[string]string) (string, string) {
	var sb strings.Builder

	pickJarFunc := randomJarByWeight
	if options["e"] != "" {
		pickJarFunc = randomJar
	}
	jar := pickJarFunc(db, jars)
	sb.WriteString(randomJarFortune(db, jar))
	return sb.String(), jar
}

func allFortunes(db *sql.DB, jar string, q string, switches map[string]string, w io.Writer) {
	var err error
	var re *regexp.Regexp
	bufw := bufio.NewWriter(w)
	defer bufw.Flush()

	sre := q
	// -i  ignore case
	if switches["i"] != "" {
		sre = "(?i)" + q
	}
	re = regexp.MustCompile(sre)

	sqlstr := fmt.Sprintf("SELECT body FROM [%s]", jar)
	rows, err := db.Query(sqlstr)
	if err != nil {
		return
	}
	for rows.Next() {
		var body string
		rows.Scan(&body)
		if !re.MatchString(body) {
			continue
		}

		if switches["c"] != "" {
			bufw.WriteString(fmt.Sprintf("(%s)\n", jar))
		}

		bufw.WriteString(body)
		// Make sure the "%" starts on a newline.
		if !strings.HasSuffix(body, "\n") {
			bufw.WriteString("\n")
		}
		bufw.WriteString("%\n")
	}
}

func allTables(db *sql.DB) []string {
	tbls := []string{}
	rows, err := db.Query(`SELECT DISTINCT tbl_name FROM sqlite_master ORDER BY tbl_name`)
	if err != nil {
		log.Fatal(err)
	}
	for rows.Next() {
		var tbl string
		err := rows.Scan(&tbl)
		if err != nil {
			return tbls
		}
		tbls = append(tbls, tbl)
	}
	return tbls
}

func queryNumRows(db *sql.DB, jar string) int {
	var rowid int
	var err error

	sqlstr := fmt.Sprintf("SELECT max(rowid) FROM [%s]", jar)
	row := db.QueryRow(sqlstr)
	err = row.Scan(&rowid)
	if err == sql.ErrNoRows {
		log.Fatal(err)
	}
	return rowid
}

func fortuneHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()

		// &outputfmt=html
		outputfmt := r.FormValue("outputfmt")

		// &sw=ec
		options := map[string]string{}
		for _, ch := range r.FormValue("sw") {
			options[string(ch)] = "y"
		}

		// &jars=perl,news
		jars := strings.Split(r.FormValue("jars"), ",")

		// Allow requests from all sites.
		w.Header().Set("Access-Control-Allow-Origin", "*")

		format := PlainText
		contentType := "text/plain"
		switch outputfmt {
		case "htmlpre":
			format = HtmlPre
			contentType = "text/html"
		case "html":
			format = Html
			contentType = "text/html"
		case "json":
			format = Json
			contentType = "application/json"
		}
		w.Header().Set("Content-Type", contentType)

		var jar string
		var jarIndex string

		// /fortune/<jar>/<index>
		// /fortune/news
		// /fortune/news/1
		sre := `^/fortune/([\w\-]+)(?:/(\d*))?$`
		re := regexp.MustCompile(sre)
		matches := re.FindStringSubmatch(r.URL.Path)
		if matches != nil {
			jar = matches[1]
			jarIndex = matches[2]
		}

		var fortune string
		if jar != "" && jarIndex != "" {
			fortune = jarFortune(db, jar, jarIndex)
		} else if jar != "" {
			fortune = randomJarFortune(db, jar)
		} else {
			fortune, jar = randomFortune(db, jars, options)
		}

		switch format {
		case PlainText:
			if options["c"] != "" {
				fmt.Fprintf(w, "(%s)\n", jar)
			}
			fmt.Fprintln(w, fortune)
		case HtmlPre:
			fmt.Fprintf(w, "<article>\n")
			fmt.Fprintf(w, "<pre>\n")
			if options["c"] != "" {
				fmt.Fprintf(w, "(%s)\n", jar)
			}
			fmt.Fprintln(w, fortune)
			fmt.Fprintf(w, "</pre>\n")
			fmt.Fprintf(w, "</article>\n")
		case Html:
			fmt.Fprintf(w, "<article class=\"fortune\">\n")
			fmt.Fprintf(w, "<p>\n")
			if options["c"] != "" {
				fmt.Fprintf(w, "(%s)<br>\n", jar)
			}
			lines := strings.Split(strings.TrimSpace(fortune), "\n")
			for _, line := range lines {
				fmt.Fprintf(w, "%s<br>\n", line)
			}
			fmt.Fprintf(w, "</p>\n")
			fmt.Fprintf(w, "</article>\n")
		}
	}
}

type FortuneData struct {
	Jar     string
	Fortune string
}

func siteHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()

		// &sw=ec
		options := map[string]string{}
		for _, ch := range r.FormValue("sw") {
			options[string(ch)] = "y"
		}

		// &jars=perl,news
		jars := strings.Split(r.FormValue("jars"), ",")

		w.Header().Set("Content-Type", "text/html")

		var jar string
		var jarIndex string

		// /fortune/(jar)
		// /fortune/(jar)/(index)
		// Ex. /news, /news/1
		sre := `^/fortune/([\w\-]+)(?:/(\d*))?$`
		re := regexp.MustCompile(sre)
		matches := re.FindStringSubmatch(r.URL.Path)
		if matches != nil {
			jar = matches[1]
			jarIndex = matches[2]
		}

		var fortune string
		if jar != "" && jarIndex != "" {
			fortune = jarFortune(db, jar, jarIndex)
		} else if jar != "" {
			fortune = randomJarFortune(db, jar)
		} else {
			fortune, jar = randomFortune(db, jars, options)
		}

		t := template.Must(template.ParseFiles("fortune.html"))
		t.Execute(w, FortuneData{Jar: jar, Fortune: fortune})
	}
}

func rootHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")

		helptext := `Help
----
<put help text here>
`
		fmt.Fprintf(w, helptext)
	}
}
