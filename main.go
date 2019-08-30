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

	_ "github.com/mattn/go-sqlite3"
)

func main() {
    var db *sql.DB
    var err error
    var cmd string

    db, err = sql.Open("sqlite3", "./fortune.db")
    if err != nil {
        log.Fatal(err)
    }

    os.Args = os.Args[1:]

    if len(os.Args) == 0 {
        cmd = "random"
    } else {
        cmd = os.Args[0]
        if cmd == "ingest" || cmd == "random" || cmd == "jars" {
            os.Args = os.Args[1:]
        } else {
            cmd = "random"
        }
    }

    switch cmd {
    case "ingest":
        for _, jarfile := range os.Args {
            ingestJarFile(db, jarfile)
        }
    case "random":
        if len(os.Args) > 0 {
            for _, jarname := range os.Args {
                fortune := randomFortune(db, jarname)
                fmt.Println(fortune)
            }
        } else {
            tbls := allTables(db)
            if len(tbls) == 0 {
                fmt.Println("No jars yet.\n Use 'ingest' to initialize one.")
                os.Exit(1)
            }

            jarname := tbls[rand.Intn(len(tbls))]
            fortune := randomFortune(db, jarname)
            fmt.Println(fortune)
        }
    case "jars":
        tbls := allTables(db)
        if len(tbls) == 0 {
            fmt.Println("No jars yet.\n Use 'ingest' to initialize one.")
            os.Exit(1)
        }

        for _, jarname := range tbls {
            fmt.Println(jarname)
        }
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
    sql = fmt.Sprintf("CREATE TABLE [%s] (id INTEGER PRIMARY KEY NOT NULL, body TEXT)", jarname)
    _, err = db.Exec(sql)

    sql = fmt.Sprintf("INSERT INTO [%s] (body) VALUES (?)", jarname)
    insertStmt, _ := db.Prepare(sql)

    scanner := bufio.NewScanner(f)
    for scanner.Scan() {
        line := scanner.Text()
        if strings.TrimSpace(line) == "%" {
            body = sb.String()
            if len(strings.TrimSpace(body)) > 0 {
                _, err = insertStmt.Exec(body)
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
    }

    _, err = db.Exec("COMMIT")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Done.\n")
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
