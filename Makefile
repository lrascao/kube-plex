GOOS=linux
GOARCH=amd64

build:
	env GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o dist/$(GOOS)/$(GOARCH)/kube-plex main.go

docker: build
	docker build -t kube-plex .
	docker tag kube-plex registry.88288338.xyz:5000/kube-plex:latest
	docker push registry.88288338.xyz:5000/kube-plex:latest
