all: fortune2

fortune2: main.go
	go build -o fortune2 main.go

clean:
	rm -rf fortune2

