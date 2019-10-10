package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
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

// This represents one fortune cookie.
// An empty Body represents a nonexisting fortune.
type Fortune struct {
	Jar  string `json:"jar"`
	ID   string `json:"id"`
	Body string `json:"body"`
}

// struct to be passed to web templates.
type FortuneCtx struct {
	Fortune
	Jars   []string
	Qjar   string
	Qjarid string
}

type JarInfo struct {
	Jar         string  `json:"jar"`
	NumFortunes int     `json:"numfortunes"`
	PctTotal    float64 `json:"pcttotal"`
}

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

		jis := jarsInfo(db, parms)
		printJarStats(os.Stdout, jis)
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
			printAllFortunes(os.Stdout, db, jar, q, switches)
		}
	case "random":
		if len(allTables(db)) == 0 {
			fmt.Println("No fortune jars yet.\n Use 'ingest' to initialize one.")
			os.Exit(1)
		}

		// -f switch comes from original 'fortune'.
		// Print jars to be searched, but don't show fortune.
		if switches["f"] != "" {
			jis := jarsInfo(db, parms)
			printJarStats(os.Stdout, jis)
			break
		}
		fortune := randomFortune(db, parms, switches)
		if switches["c"] != "" {
			fmt.Printf("(%s)\n", fortune.Jar)
		}
		fmt.Println(fortune.Body)
	case "serve":
		port := "8000"
		if len(parms) > 0 {
			port = parms[0]
		}
		http.Handle("/asset/", http.StripPrefix("/asset/", http.FileServer(http.Dir("./asset"))))
		http.HandleFunc("/info/", infoHandler(db))
		http.HandleFunc("/fortune/", fortuneHandler(db))
		http.HandleFunc("/fortuneweb/", fortunewebHandler(db))
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

func jarsInfo(db *sql.DB, jars []string) []JarInfo {
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

	jis := []JarInfo{}
	for _, jar := range jars {
		nRows := jarNumRows[jar]
		pctTotal := 0.0
		if totalRows > 0 {
			pctTotal = float64(nRows) / float64(totalRows) * 100
		}
		jis = append(jis, JarInfo{Jar: jar, NumFortunes: nRows, PctTotal: pctTotal})
	}

	return jis
}

func printJarStats(w io.Writer, jis []JarInfo) {
	bufw := bufio.NewWriter(w)
	defer bufw.Flush()

	fmt.Fprintf(bufw, "%-20s  %8s  %6s\n", "Fortune Jar", "# fortunes", "%")
	fmt.Fprintf(bufw, "%-20s  %8s  %6s\n", strings.Repeat("-", 20), strings.Repeat("-", 10), strings.Repeat("-", 6))

	for _, ji := range jis {
		fmt.Fprintf(bufw, "%-20s  %10d  %6.2f\n", ji.Jar, ji.NumFortunes, ji.PctTotal)
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

func randomJarFortune(db *sql.DB, jar string) Fortune {
	var fortune Fortune

	sqlstr := fmt.Sprintf("SELECT id, body FROM [%s] WHERE rowid = (abs(random()) %% (SELECT max(rowid) FROM [%s]) + 1)", jar, jar)
	row := db.QueryRow(sqlstr)
	row.Scan(&fortune.ID, &fortune.Body)
	fortune.Jar = jar

	return fortune
}

func jarFortune(db *sql.DB, jar string, jarIndex string) Fortune {
	var fortune Fortune

	sqlstr := fmt.Sprintf("SELECT id, body FROM [%s] WHERE rowid = %s", jar, jarIndex)
	row := db.QueryRow(sqlstr)
	row.Scan(&fortune.ID, &fortune.Body)
	fortune.Jar = jar

	return fortune
}

func randomFortune(db *sql.DB, jars []string, options map[string]string) Fortune {
	pickJarFunc := randomJarByWeight
	if options["e"] != "" {
		pickJarFunc = randomJar
	}
	jar := pickJarFunc(db, jars)
	return randomJarFortune(db, jar)
}

func printAllFortunes(w io.Writer, db *sql.DB, jar string, q string, switches map[string]string) {
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
		// ?jars=jar1,jar2,jar3
		// &w=ec
		// &utputfmt=json
		r.ParseForm()
		outputfmt := r.FormValue("outputfmt")
		options := map[string]string{}
		for _, ch := range r.FormValue("sw") {
			options[string(ch)] = "y"
		}
		var jars []string
		if r.FormValue("jars") != "" {
			jars = strings.Split(r.FormValue("jars"), ",")
		}

		// Allow requests from all sites.
		w.Header().Set("Access-Control-Allow-Origin", "*")

		contentType := "text/plain"
		switch outputfmt {
		case "htmlpre":
			contentType = "text/html"
		case "html":
			contentType = "text/html"
		case "json":
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

		var fortune Fortune
		if jar != "" && jarIndex != "" {
			fortune = jarFortune(db, jar, jarIndex)
		} else if jar != "" {
			fortune = randomJarFortune(db, jar)
		} else {
			fortune = randomFortune(db, jars, options)
		}

		switch outputfmt {
		case "htmlpre":
			fmt.Fprintf(w, "<article>\n")
			fmt.Fprintf(w, "<pre>\n")
			if options["c"] != "" {
				fmt.Fprintf(w, "(%s)\n", fortune.Jar)
			}
			fmt.Fprintln(w, fortune.Body)
			fmt.Fprintf(w, "</pre>\n")
			fmt.Fprintf(w, "</article>\n")
		case "html":
			fmt.Fprintf(w, "<article class=\"fortune\">\n")
			fmt.Fprintf(w, "<p>\n")
			if options["c"] != "" {
				fmt.Fprintf(w, "(%s)<br>\n", fortune.Jar)
			}
			lines := strings.Split(strings.TrimSpace(fortune.Body), "\n")
			for _, line := range lines {
				fmt.Fprintf(w, "%s<br>\n", line)
			}
			fmt.Fprintf(w, "</p>\n")
			fmt.Fprintf(w, "</article>\n")
		case "json":
			b, _ := json.MarshalIndent(fortune, "", "\t")
			w.Write(b)
		default:
			if options["c"] != "" {
				fmt.Fprintf(w, "(%s)\n", fortune.Jar)
			}
			fmt.Fprintln(w, fortune.Body)
		}
	}
}

func fortunewebHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var jar string
		var jarid string

		// ?jar=<abc>&jarid=<nnn>
		// Ex. ?jar=fortunes&jarid=2
		r.ParseForm()
		jar = r.FormValue("jar")
		jarid = r.FormValue("jarid")

		if jar == "(random)" {
			jar = ""
		}

		w.Header().Set("Content-Type", "text/html")

		var fortune Fortune
		if jar != "" && jarid != "" {
			fortune = jarFortune(db, jar, jarid)
		} else if jar != "" {
			fortune = randomJarFortune(db, jar)
		} else {
			fortune = randomFortune(db, nil, nil)
		}

		printFortunePage(w, fortune, allTables(db), jar, jarid)
	}
}

func rootHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// &outputfmt=json
		r.ParseForm()
		outputfmt := r.FormValue("outputfmt")

		if outputfmt == "html" {
			w.Header().Set("Content-Type", "text/html")
			t := template.Must(template.ParseFiles("help.html"))
			t.Execute(w, nil)
		} else {
			w.Header().Set("Content-Type", "text/plain")
			t := template.Must(template.ParseFiles("help.txt"))
			t.Execute(w, nil)
		}
	}
}

func infoHandler(db *sql.DB) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// ?jars=jar1,jar2,jar3
		// &outputfmt=json
		r.ParseForm()
		outputfmt := r.FormValue("outputfmt")
		var jars []string
		if r.FormValue("jars") != "" {
			jars = strings.Split(r.FormValue("jars"), ",")
		}

		w.Header().Set("Content-Type", "application/json")

		jis := jarsInfo(db, jars)
		switch outputfmt {
		case "json":
			b, _ := json.MarshalIndent(jis, "", "\t")
			w.Write(b)
		default:
			printJarStats(w, jis)
		}
	}
}

func printFortunePage(w io.Writer, fortune Fortune, jars []string, qjar, qjarid string) {
	fmt.Fprintln(w, `<!DOCTYPE HTML>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>fortune2 test page</title>
<style>
body { margin: 0 auto; width: 50% }
.fortune { padding: 20px; border: 1px dotted; }
#new_fortune { margin: 20px 0; }
.fortune {margin: 1em 0;}
</style>
</head>
<body>`)
	fmt.Fprintln(w, `<h1>Get your Fortune</h1>`)
	fmt.Fprintln(w, `<form action="./" method="get">`)
	fmt.Fprintln(w, `<label for="jar">Select a category</label><br>`)
	if qjar != "" {
		fmt.Fprintf(w, `<input id="jar" name="jar" list="jarslist" value="%s">`, qjar)
	} else {
		fmt.Fprintf(w, `<input id="jar" name="jar" list="jarslist" value="(random)">`)
	}
	fmt.Fprintln(w, "")

	fmt.Fprintln(w, `<datalist id="jarslist">`)
	fmt.Fprintln(w, `   <option value="(random)">`)
	for _, jar := range jars {
		fmt.Fprintf(w, `<option value="%s">`, jar)
		fmt.Fprintln(w, "")
	}
	fmt.Fprintln(w, `</datalist>`)
	fmt.Fprintln(w, `<button id="get_fortune">Get Fortune</button>`)
	fmt.Fprintln(w, `</form>`)

	if fortune.Body != "" {
		fmt.Fprintln(w, `<article class="fortune">`)
		fmt.Fprintln(w, `<p>`)
		fmt.Fprintf(w, `(%s)<br>`, fortune.Jar)
		fmt.Fprintln(w, "")
		fmt.Fprintf(w, `%s<br>`, fortune.Body)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, `</p>`)

		if qjarid == "" {
			fmt.Fprintf(w, `<p><a href="/site?jar=%s&jarid=%s">permalink</a></p>`, fortune.Jar, fortune.ID)
			fmt.Fprintln(w, "")
		}
		fmt.Fprintln(w, `</article>`)
	} else {
		fmt.Fprintln(w, `<p>No fortune exists.</p>`)
	}

	fmt.Fprintln(w, `<script>
let jar_entry = document.querySelector("#jar");
jar_entry.addEventListener("focus", function(e) {
    jar_entry.select();
});
</script>`)
	fmt.Fprintln(w, `</body>`)
	fmt.Fprintln(w, `</html>`)
}
