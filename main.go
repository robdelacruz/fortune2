package main

import (
	"bufio"
	"database/sql"
	"fmt"
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
		for _, jarname := range parms {
			deleteJar(db, jarname)
		}
	case "search":
		var q string
		var jarnames []string

		if len(parms) > 0 {
			q = parms[0]
			parms = parms[1:]
			jarnames = parms
		}
		if len(jarnames) == 0 {
			jarnames = allTables(db)
		}
		for _, jarname := range jarnames {
			allFortunes(db, jarname, q, switches, os.Stdout)
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

		pickJarFunc := randomJarByWeight
		if switches["e"] != "" {
			pickJarFunc = randomJar
		}
		jarname := pickJarFunc(db, parms)
		if switches["c"] != "" {
			fmt.Printf("(%s)\n", jarname)
		}
		fmt.Println(randomFortune(db, jarname))
	case "serve":
		port := "8000"
		if len(parms) > 0 {
			port = parms[0]
		}
		fmt.Printf("Listening on %s...\n", port)
		http.Handle("/web/", http.StripPrefix("/web/", http.FileServer(http.Dir("./web"))))
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			r.ParseForm()
			sw := r.FormValue("sw")
			qjars := r.FormValue("jars")
			outputfmt := r.FormValue("outputfmt")

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

			jarnames := []string{}
			if qjars != "" {
				jarnames = strings.Split(r.FormValue("jars"), ",")
			}

			pickJarFunc := randomJarByWeight
			if strings.ContainsAny(sw, "e") {
				pickJarFunc = randomJar
			}

			jarname := pickJarFunc(db, jarnames)
			fortune := randomFortune(db, jarname)

			switch format {
			case PlainText:
				if strings.ContainsAny(sw, "c") {
					fmt.Fprintf(w, "(%s)\n", jarname)
				}
				fmt.Fprintf(w, fortune)
				fmt.Fprintf(w, "\n")
			case HtmlPre:
				fmt.Fprintf(w, "<article>\n")
				fmt.Fprintf(w, "<pre>\n")
				if strings.ContainsAny(sw, "c") {
					fmt.Fprintf(w, "(%s)\n", jarname)
				}
				fmt.Fprintf(w, fortune)
				fmt.Fprintf(w, "</pre>\n")
				fmt.Fprintf(w, "</article>\n")
			case Html:
				fmt.Fprintf(w, "<article>\n")
				if strings.ContainsAny(sw, "c") {
					fmt.Fprintf(w, "<p>(%s)</p>\n", jarname)
				}
				lines := strings.Split(strings.TrimSpace(fortune), "\n")
				for _, line := range lines {
					fmt.Fprintf(w, "<p>%s</p>\n", line)
				}
				fmt.Fprintf(w, "</article>\n")
			}

		})
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

func printJarStats(db *sql.DB, jarnames []string) {
	if len(jarnames) == 0 {
		jarnames = allTables(db)
	}

	jarNumRows := map[string]int{}
	var totalRows int
	for _, jarname := range jarnames {
		nRows := queryNumRows(db, jarname)
		jarNumRows[jarname] = nRows
		totalRows += nRows
	}

	fmt.Printf("%-20s  %8s  %6s\n", "Fortune Jar", "fortunes", "%")
	fmt.Printf("%-20s  %8s  %6s\n", strings.Repeat("-", 20), strings.Repeat("-", 8), strings.Repeat("-", 6))
	for _, jarname := range jarnames {
		nRows := jarNumRows[jarname]
		pctTotal := 0.0
		if totalRows > 0 {
			pctTotal = float64(nRows) / float64(totalRows) * 100
		}
		fmt.Printf("%-20s  %8d  %6.2f\n", jarname, nRows, pctTotal)
	}
}

func ingestJarFile(db *sql.DB, jarfile string) {
	var jarname, sql string
	var sb strings.Builder
	var body string

	f, err := os.Open(jarfile)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	jarname = strings.Split(filepath.Base(jarfile), ".")[0]
	fmt.Printf("Writing '%s' to table '%s'...", jarfile, jarname)

	_, err = db.Exec("BEGIN TRANSACTION")
	if err != nil {
		log.Fatal(err)
	}

	sql = fmt.Sprintf("DROP TABLE IF EXISTS [%s]", jarname)
	_, err = db.Exec(sql)
	if err != nil {
		log.Fatal(err)
	}
	sql = fmt.Sprintf("CREATE TABLE [%s] (id INTEGER PRIMARY KEY NOT NULL, body TEXT)", jarname)
	_, err = db.Exec(sql)
	if err != nil {
		log.Fatal(err)
	}

	sql = fmt.Sprintf("INSERT INTO [%s] (body) VALUES (?)", jarname)
	insertStmt, err := db.Prepare(sql)
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

func deleteJar(db *sql.DB, jarname string) {
	var err error
	var sqlstr string

	_, err = db.Exec("BEGIN TRANSACTION")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Deleting jar '%s'...", jarname)
	sqlstr = fmt.Sprintf("DROP TABLE IF EXISTS [%s]", jarname)
	_, err = db.Exec(sqlstr)

	_, err = db.Exec("COMMIT")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Done.\n")
}

func randomJar(db *sql.DB, jarnames []string) string {
	rand.Seed(time.Now().UnixNano())

	if len(jarnames) == 0 {
		jarnames = allTables(db)
	}

	return jarnames[rand.Intn(len(jarnames))]
}

func randomJarByWeight(db *sql.DB, jarnames []string) string {
	rand.Seed(time.Now().UnixNano())

	if len(jarnames) == 0 {
		jarnames = allTables(db)
	}

	jarNumRows := map[string]int{}
	var totalRows int
	for _, jarname := range jarnames {
		nRows := queryNumRows(db, jarname)
		jarNumRows[jarname] = nRows
		totalRows += nRows
	}

	// None of the jarnames exist, so just select from all jars.
	if totalRows == 0 {
		return randomJar(db, allTables(db))
	}

	npick := rand.Intn(totalRows)

	var pickJar string
	var sumRows int
	for _, jarname := range jarnames {
		sumRows += jarNumRows[jarname]
		if npick < sumRows {
			pickJar = jarname
			break
		}
	}
	if pickJar == "" {
		log.Fatalf("No jar was picked. totalRows=%d npick=%d", totalRows, npick)
	}
	return pickJar
}

func randomFortune(db *sql.DB, jarname string) string {
	var body string
	var err error

	sqlstr := fmt.Sprintf("SELECT body FROM [%s] WHERE rowid = (abs(random()) %% (SELECT max(rowid) FROM [%s]) + 1)", jarname, jarname)
	row := db.QueryRow(sqlstr)
	err = row.Scan(&body)
	if err == sql.ErrNoRows {
		log.Fatal(err)
	}
	return body
}

func allFortunes(db *sql.DB, jarname string, q string, switches map[string]string, w io.Writer) {
	var body string
	var err error
	var re *regexp.Regexp
	bufw := bufio.NewWriter(w)

	sre := q
	// -i  ignore case
	if switches["i"] != "" {
		sre = "(?i)" + q
	}
	re = regexp.MustCompile(sre)

	sqlstr := fmt.Sprintf("SELECT body FROM [%s]", jarname)
	rows, err := db.Query(sqlstr)
	if err != nil {
		log.Fatal(err)
	}
	for rows.Next() {
		err = rows.Scan(&body)
		if err != nil {
			log.Fatal(err)
		}

		if !re.MatchString(body) {
			continue
		}
		_, err = bufw.WriteString(body)
		if err != nil {
			log.Fatal(err)
		}
		// Make sure the "%" starts on a newline.
		if !strings.HasSuffix(body, "\n") {
			bufw.WriteString("\n")
		}
		bufw.WriteString("%\n")
	}
	bufw.Flush()
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

func queryNumRows(db *sql.DB, jarname string) int {
	var rowid int
	var err error

	sqlstr := fmt.Sprintf("SELECT max(rowid) FROM [%s]", jarname)
	row := db.QueryRow(sqlstr)
	err = row.Scan(&rowid)
	if err == sql.ErrNoRows {
		log.Fatal(err)
	}
	return rowid
}
