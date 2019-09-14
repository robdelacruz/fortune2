SHAREDIR=/usr/local/share/fortune2
BINDIR=/usr/local/bin

all: fortune2

dep:
	go get -u github.com/mattn/go-sqlite3

fortune2: main.go
	go build -o fortune2 main.go

clean:
	rm -rf fortune2

install: fortune2
	mkdir -p $(SHAREDIR)
	touch $(SHAREDIR)/fortune2.db
	chmod a+w $(SHAREDIR)
	chmod a+w $(SHAREDIR)/fortune2.db
	cp fortune2 $(BINDIR)

uninstall:
	rm -rf $(SHAREDIR)
	rm -rf $(BINDIR)/fortune2

install_fortunes:
	fortune2 ingest fortunes/*

