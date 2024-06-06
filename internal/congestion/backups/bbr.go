package congestion

import (
	"time"

	"github.com/lucas-clemente/quic-go/internal/protocol"
)

// This hybla implementation is based on the one found in Chromiums's QUIC
// implementation, in the files net/quic/congestion_control/cubih.{hh,cc}.

// Constants based on TCP defaults.
// The following constants are in 2^10 fractions of a second instead of ms to
// allow a 10 shift right to divide.

const defaultNumConnections = 1
const backoffFactor = 1
const referenceRtt = 25

// hybla implements the hybla algorithm from TCP
type BBR struct {
	clock Clock

	// Number of connections to simulate.
	numConnections int

	// Time when this cycle started, after last loss event.
	epoch time.Time

	// Max congestion window used just before last loss event.
	// Note: to improve fairness to other streams an additional back off is
	// applied to this value if the new value is below our latest value.
	lastMaxCongestionWindow protocol.ByteCount

	// Number of acked bytes since the cycle started (epoch).
	ackedBytesCount protocol.ByteCount

	// TCP Reno equivalent congestion window in packets.
	estimatedTCPcongestionWindow protocol.ByteCount

	// Origin point of hybla function.
	originPointCongestionWindow protocol.ByteCount

	// Time to origin point of hybla function in 2^10 fractions of a second.
	timeToOriginPoint uint32

	// Last congestion window in packets computed by hybla function.
	lastTargetCongestionWindow protocol.ByteCount
	increaseFlag               int
	epochCounter               int
}

// Newhybla returns a new hybla instance
func NewBBR(clock Clock) *BBR {
	b := &BBR{
		clock:          clock,
		numConnections: defaultNumConnections,
		epochCounter:   0,
		increaseFlag:   0,
	}
	b.Reset()
	return b
}

// Reset is called after a timeout to reset the hybla state
func (b *BBR) Reset() {
	b.epoch = time.Time{}
	b.epochCounter = 0
	b.lastMaxCongestionWindow = 0
	b.ackedBytesCount = 0
	b.estimatedTCPcongestionWindow = 0
	b.originPointCongestionWindow = 0
	b.timeToOriginPoint = 0
	b.lastTargetCongestionWindow = 0
}

// OnApplicationLimited is called on ack arrival when sender is unable to use
// the available congestion window. Resets hybla state during quiescence.
func (b *BBR) OnApplicationLimited() {
	// When sender is not using the available congestion window, the window does
	// not grow. But to be RTT-independent, hybla assumes that the sender has been
	// using the entire window during the time since the beginning of the current
	// "epoch" (the end of the last loss recovery period). Since
	// application-limited periods break this assumption, we reset the epoch when
	// in such a period. This reset effectively freezes congestion window growth
	// through application-limited periods and allows hybla growth to continue
	// when the entire window is being used.
	b.epoch = time.Time{}
}

// CongestionWindowAfterPacketLoss computes a new congestion window to use after
// a loss event. Returns the new congestion window in packets. The new
// congestion window is a multiplicative decrease of our current window.
func (b *BBR) CongestionWindowAfterPacketLoss(currentCongestionWindow protocol.ByteCount) protocol.ByteCount {
	b.epoch = time.Time{} // Reset time.
	return protocol.ByteCount(float32(currentCongestionWindow) * backoffFactor)
}

// CongestionWindowAfterAck computes a new congestion window to use after a received ACK.
// Returns the new congestion window in packets. The new congestion window
// follows a hybla function that depends on the time passed since last
// packet loss.
func (b *BBR) CongestionWindowAfterAck(
	ackedBytes protocol.ByteCount,
	currentCongestionWindow protocol.ByteCount,
	eventTime time.Time,
	bandwidthEstimate protocol.ByteCount,
) protocol.ByteCount {
	b.ackedBytesCount += ackedBytes
	if b.epoch.IsZero() {
		// First ACK after a loss event.
		b.epoch = eventTime            // Start of epoch.
		b.ackedBytesCount = ackedBytes // Reset count.
		// Reset estimated_tcp_congestion_window_ to be in sync with cubic.
		b.estimatedTCPcongestionWindow = currentCongestionWindow
	}

	// Limit the CWND increase to half the acked bytes.
	targetCongestionWindow := bandwidthEstimate
	b.ackedBytesCount = 0

	// We have a new hybla congestion window.
	b.lastTargetCongestionWindow = targetCongestionWindow

	// Compute target congestion_window based on hybla target and estimated TCP
	// congestion_window, use highest (fastest).
	if targetCongestionWindow < b.estimatedTCPcongestionWindow {
		//targetCongestionWindow = b.estimatedTCPcongestionWindow
	}
	return protocol.ByteCount(uint32(targetCongestionWindow))
}

// SetNumConnections sets the number of emulated connections
func (b *BBR) SetNumConnections(n int) {
	b.numConnections = n
}
