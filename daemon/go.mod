module codeterminal/daemon

go 1.23

require codeterminal/protocol v0.0.0

require (
	codeterminal/helper v0.0.0
	github.com/philippgille/chromem-go v0.7.0
)

replace codeterminal/protocol => ../protocol

replace codeterminal/helper => ../helper
