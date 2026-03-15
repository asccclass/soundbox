# 設定目標為 Linux 64位元 ARM
buildArm:
	set CGO_ENABLED=0&& set GOOS=linux&& set GOARCH=arm64&& go build -o soundbox ./...

# 設定目標為 Windows 64位元
buildWin:
	set CGO_ENABLED=0&& set GOOS=windows&& set GOARCH=amd64&& go build -o soundbox.exe ./...
