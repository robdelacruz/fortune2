package main

import (
    "bufio"
    "fmt"
    "log"
    "strings"
    "os"
    "database/sql"
    "path/filepath"
    "math/rand"
    "time"

	_ "github.com/mattn/go-sqlite3"
)

func main() {
    var db *sql.DB
    var err error
    var cmd string

    // Use $FORTUNE2FILE or /usr/local/share/fortune2/fortune2.db if env var not defined.
    fortunefile := os.Getenv("FORTUNE2FILE")
    if fortunefile == "" {
        dirpath := filepath.Join(string(os.PathSeparator), "usr", "local", "share", "fortune2")
        os.MkdirAll(dirpath, os.ModePerm)
        fortunefile = filepath.Join(dirpath, "fortune2.db")
    }
    db, err = sql.Open("sqlite3", fortunefile)
    if err != nil {
        log.Fatal(err)
    }

    os.Args = os.Args[1:]

    cmd = "random"
    if len(os.Args) > 0 {
        if os.Args[0] == "ingest" || os.Args[0] == "delete" || os.Args[0] == "info" || os.Args[0] == "random" {
            cmd = os.Args[0]
            os.Args = os.Args[1:]
        }
    }

    switches, parms := parseArgs(os.Args)

    switch cmd {
    case "ingest":
        for _, jarfile := range parms {
            ingestJarFile(db, jarfile)
        }
    case "delete":
        for _, jarname := range parms {
            deleteJar(db, jarname)
        }
    case "random":
        if len(allTables(db)) == 0 {
            fmt.Println("No fortune jars yet.\n Use 'ingest' to initialize one.")
            os.Exit(1)
        }

        var pickJar string
        if switches["w"] != "" {
            pickJar = randomJarByWeight(db, parms)
        } else {
            pickJar = randomJar(db, parms)
        }
        fortune := randomFortune(db, pickJar)
        if switches["j"] != "" || switches["jar"] != "" {
            fmt.Printf("(%s)\n", pickJar)
        }
        fmt.Println(fortune)
    case "info":
        fmt.Printf("fortune db file:  %s\n\n", fortunefile)
        tbls := allTables(db)
        if len(tbls) == 0 {
            fmt.Println("No fortune jars yet.\n Use 'ingest' to initialize one.")
            os.Exit(1)
        }

        jarNumRows := map[string]int{}
        var totalRows int
        for _, jarname := range tbls {
            nRows := queryNumRows(db, jarname)
            jarNumRows[jarname] = nRows
            totalRows += nRows
        }

        fmt.Printf("%-20s  %8s  %6s\n", "Fortune Jar", "fortunes", "%")
        fmt.Printf("%-20s  %8s  %6s\n", strings.Repeat("-", 20), strings.Repeat("-", 8), strings.Repeat("-", 6))
        for _, jarname := range tbls {
            nRows := jarNumRows[jarname]
            pctTotal := float64(nRows) / float64(totalRows) * 100
            fmt.Printf("%-20s  %8d  %6.2f\n", jarname, nRows, pctTotal)
        }
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

    standaloneSwitches := []string{"j", "w"}
    definitionSwitches := []string{}
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
            if listContains(standaloneSwitches, arg[1:]) {
                // -j -w
                switches[arg[1:]] = "y"
            } else if listContains(definitionSwitches, arg[1:]) {
                // -key "val"
                curKey = arg[1:]
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
        panic(err)
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
            if len(strings.TrimSpace(body)) > 0 {
                _, err = insertStmt.Exec(body)
                if err != nil {
                    log.Fatal(err)
                }
            }
            sb.Reset()
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

func allTables(db *sql.DB) []string {
	tbls := []string{}
	rows, err := db.Query(`SELECT DISTINCT tbl_name FROM sqlite_master ORDER BY tbl_name`)
	if err != nil {
		return tbls
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

