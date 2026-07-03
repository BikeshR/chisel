package tui

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BikeshR/chisel/internal/agent"
)

var errStreamClosed = errors.New("stream closed unexpectedly")

// startStream kicks off one streamed API call and returns a Cmd that waits
// for the first event. Each subsequent event is delivered the same way:
// Update receives a streamEventMsg and, if the stream isn't done, calls
// waitForChunk again to keep listening.
func startStream(client *agent.Client, messages []agent.Message) tea.Cmd {
	return func() tea.Msg {
		ch, err := client.SendStreaming(context.Background(), messages)
		if err != nil {
			return streamEventMsg{event: agent.Event{Done: true, Err: err}}
		}
		return waitForChunk(ch)()
	}
}

func waitForChunk(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamEventMsg{event: agent.Event{Done: true, Err: errStreamClosed}}
		}
		return streamEventMsg{event: ev, ch: ch}
	}
}
