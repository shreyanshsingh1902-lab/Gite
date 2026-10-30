package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

func client(user string, route string, keyPath string) {
	// keyPath := os.ExandEnv(path) // pvt keyPath
	key, err := os.ReadFile(keyPath)
	if err != nil {
		log.Fatalf("unable to read private key: %v", err)
	}

	// Read Key (Parse)
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		log.Fatalf("unable to parse private key: %v", err)
	}

	// Configure ssh client
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	client, err := ssh.Dial("tcp", route, config)
	if err != nil {
		log.Fatalf("failed to dial: %v", err)
	}
	defer client.Close()
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}

		cmd := strings.TrimSpace(scanner.Text())
		if cmd == "" {
			continue
		}

		session, err := client.NewSession()
		if err != nil {
			log.Printf("failed to create session: %v", err)
			continue
		}

		output, err := session.CombinedOutput(cmd)
		if err != nil {
			log.Printf("failed to run command: %v", err)
			session.Close()
			continue
		}

		fmt.Print(string(output))
		session.Close()
	}
}

func main() {
	client("majinBuu", "127.0.0.1:2222", ".temp/sshKey")
}
