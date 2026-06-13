package proxy

import (
	"fmt"
	"net"
	"strings"

	"seedstream/pkg/usenet/pool"
)

type Session struct {
	conn     net.Conn
	usenet   *pool.Pool
	authUser string
	authPass string

	authenticated bool
	currentGroup  string
	shouldQuit    bool
}

func NewSession(conn net.Conn, usenet *pool.Pool, authUser, authPass string) *Session {
	return &Session{
		conn:          conn,
		usenet:        usenet,
		authUser:      authUser,
		authPass:      authPass,
		authenticated: authUser == "",
	}
}

func (s *Session) WriteLine(line string) error {
	_, err := fmt.Fprintf(s.conn, "%s\r\n", line)
	return err
}

func (s *Session) WriteMultiLine(lines []string) error {
	for _, line := range lines {

		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		if err := s.WriteLine(line); err != nil {
			return err
		}
	}

	return s.WriteLine(".")
}

func (s *Session) ShouldQuit() bool {
	return s.shouldQuit
}

func (s *Session) CurrentGroup() string {
	return s.currentGroup
}

func (s *Session) HandleCommand(cmd string, args []string) error {

	switch cmd {
	case "QUIT":
		return s.handleQuit(args)
	case "CAPABILITIES":
		return s.handleCapabilities(args)
	case "AUTHINFO":
		return s.handleAuthInfo(args)
	}

	if !s.authenticated {
		return s.WriteLine("480 Authentication required")
	}

	switch cmd {
	case "GROUP":
		return s.handleGroup(args)
	case "ARTICLE":
		return s.handleArticle(args)
	case "BODY":
		return s.handleBody(args)
	case "HEAD":
		return s.handleHead(args)
	case "STAT":
		return s.handleStat(args)
	case "LIST":
		return s.handleList(args)
	case "DATE":
		return s.handleDate(args)
	case "MODE":

		if len(args) >= 1 && strings.ToUpper(args[0]) == "READER" {
			return s.WriteLine("201 SeedStream proxy (reader mode)")
		}
		return s.WriteLine(fmt.Sprintf("500 Unknown command: %s", cmd))
	default:
		return s.WriteLine(fmt.Sprintf("500 Unknown command: %s", cmd))
	}
}
