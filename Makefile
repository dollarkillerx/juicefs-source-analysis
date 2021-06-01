/usr/local/include/winfsp:
	sudo mkdir -p /usr/local/include/winfsp
	sudo cp hack/winfsp_headers/* /usr/local/include/winfsp

# sudo apt-get update && sudo apt-get install libfuse-dev
# GOOS=windows go build -buildmode exe cgofuse.go