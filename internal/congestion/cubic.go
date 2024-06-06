/* package congestion

import (
	"log"
	"time"

	"github.com/lucas-clemente/quic-go/internal/protocol"
	"github.com/lucas-clemente/quic-go/internal/utils"
)

// This hybla implementation is based on the one found in Chromiums's QUIC
// implementation, in the files net/quic/congestion_control/cubih.{hh,cc}.

// Constants based on TCP defaults.
// The following constants are in 2^10 fractions of a second instead of ms to
// allow a 10 shift right to divide.

const defaultNumConnections = 1
const backoffFactor = 0.5
const referenceRtt = 25
const beta float32 = 0.7

// hybla implements the hybla algorithm from TCP
type Hybla struct {
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
}

// Newhybla returns a new hybla instance
func NewHybla(clock Clock) *Hybla {
	h := &Hybla{
		clock:          clock,
		numConnections: defaultNumConnections,
	}
	h.Reset()
	return h
}

// Reset is called after a timeout to reset the hybla state
func (h *Hybla) Reset() {
	h.epoch = time.Time{}
	h.lastMaxCongestionWindow = 0
	h.ackedBytesCount = 0
	h.estimatedTCPcongestionWindow = 0
	h.originPointCongestionWindow = 0
	h.timeToOriginPoint = 0
	h.lastTargetCongestionWindow = 0
}

func (h *Hybla) alpha() float32 {
	// TCPFriendly alpha is described in Section 3.3 of the hybla paper. Note that
	// beta here is a cwnd multiplier, and is equal to 1-beta from the paper.
	// We derive the equivalent alpha for an N-connection emulation as:
	b := h.beta()
	return 3 * float32(h.numConnections) * float32(h.numConnections) * (1 - b) / (1 + b)
}

// OnApplicationLimited is called on ack arrival when sender is unable to use
// the available congestion window. Resets hybla state during quiescence.
func (h *Hybla) OnApplicationLimited() {
	// When sender is not using the available congestion window, the window does
	// not grow. But to be RTT-independent, hybla assumes that the sender has been
	// using the entire window during the time since the beginning of the current
	// "epoch" (the end of the last loss recovery period). Since
	// application-limited periods break this assumption, we reset the epoch when
	// in such a period. This reset effectively freezes congestion window growth
	// through application-limited periods and allows hybla growth to continue
	// when the entire window is being used.
	h.epoch = time.Time{}
}

func (h *Hybla) beta() float32 {
	// kNConnectionBeta is the backoff factor after loss for our N-connection
	// emulation, which emulates the effective backoff of an ensemble of N
	// TCP-Reno connections on a single loss event. The effective multiplier is
	// computed as:
	return (float32(h.numConnections) - 1 + beta) / float32(h.numConnections)
}

// CongestionWindowAfterPacketLoss computes a new congestion window to use after
// a loss event. Returns the new congestion window in packets. The new
// congestion window is a multiplicative decrease of our current window.
func (h *Hybla) CongestionWindowAfterPacketLoss(currentCongestionWindow protocol.ByteCount) protocol.ByteCount {
	h.epoch = time.Time{} // Reset time.
	return protocol.ByteCount(float32(currentCongestionWindow) * backoffFactor)
}

// CongestionWindowAfterAck computes a new congestion window to use after a received ACK.
// Returns the new congestion window in packets. The new congestion window
// follows a hybla function that depends on the time passed since last
// packet loss.
func (h *Hybla) CongestionWindowAfterAck(
	ackedBytes protocol.ByteCount,
	currentCongestionWindow protocol.ByteCount,
	delayMin time.Duration,
	eventTime time.Time,
	p uint32,
) protocol.ByteCount {
	h.ackedBytesCount += ackedBytes
	//log.Printf("CONGESTION SPEAKING\n")
	if h.epoch.IsZero() {
		// First ACK after a loss event.
		h.epoch = eventTime            // Start of epoch.
		h.ackedBytesCount = ackedBytes // Reset count.
		// Reset estimated_tcp_congestion_window_ to be in sync with cubih.
		h.estimatedTCPcongestionWindow = currentCongestionWindow
	}

	// Limit the CWND increase to half the acked bytes.
	targetCongestionWindow := utils.MinByteCount(currentCongestionWindow+protocol.ByteCount(uint32(protocol.DefaultTCPMSS)*((p*p)/uint32(currentCongestionWindow))), currentCongestionWindow+h.ackedBytesCount/2)

	// Increase the window by approximately Alpha * 1 MSS of bytes every
	// time we ack an estimated tcp window of bytes.  For small
	// congestion windows (less than 25), the formula below will
	// increase slightly slower than linearly per estimated tcp window
	// of bytes.
	h.estimatedTCPcongestionWindow += protocol.ByteCount(float32(h.ackedBytesCount) * h.alpha() * float32(protocol.DefaultTCPMSS) / float32(h.estimatedTCPcongestionWindow))
	h.ackedBytesCount = 0
	log.Printf("alpha is: %d\n", h.alpha)

	// We have a new hybla congestion window.
	h.lastTargetCongestionWindow = targetCongestionWindow

	// Compute target congestion_window based on hybla target and estimated TCP
	// congestion_window, use highest (fastest).
	if targetCongestionWindow < h.estimatedTCPcongestionWindow {
		targetCongestionWindow = h.estimatedTCPcongestionWindow
	}
	return targetCongestionWindow
}

// SetNumConnections sets the number of emulated connections
func (h *Hybla) SetNumConnections(n int) {
	h.numConnections = n
} */

/* package congestion

import (
	"log"
	"math"
	"time"

	"github.com/lucas-clemente/quic-go/internal/protocol"
	"github.com/lucas-clemente/quic-go/internal/utils"
)

// This cubic implementation is based on the one found in Chromiums's QUIC
// implementation, in the files net/quic/congestion_control/cubic.{hh,cc}.

// Constants based on TCP defaults.
// The following constants are in 2^10 fractions of a second instead of ms to
// allow a 10 shift right to divide.

// 1024*1024^3 (first 1024 is from 0.100^3)
// where 0.100 is 100 ms which is the scaling round trip time.
const cubeScale = 40
const cubeCongestionWindowScale = 410
const cubeFactor protocol.ByteCount = 1 << cubeScale / cubeCongestionWindowScale / protocol.DefaultTCPMSS

const defaultNumConnections = 1

// Default Cubic backoff factor
const beta float32 = 0.7

// Additional backoff factor when loss occurs in the concave part of the Cubic
// curve. This additional backoff factor is expected to give up bandwidth to
// new concurrent flows and speed up convergence.
const betaLastMax float32 = 0.85

// Cubic implements the cubic algorithm from TCP
type Cubic struct {
	clock Clock

	// Number of connections to simulate.
	numConnections int

	// Time when this cycle started, after last loss event.
	epoch        time.Time
	epochCounter int

	// Max congestion window used just before last loss event.
	// Note: to improve fairness to other streams an additional back off is
	// applied to this value if the new value is below our latest value.
	lastMaxCongestionWindow protocol.ByteCount

	// Number of acked bytes since the cycle started (epoch).
	ackedBytesCount protocol.ByteCount

	// TCP Reno equivalent congestion window in packets.
	estimatedTCPcongestionWindow protocol.ByteCount

	// Origin point of cubic function.
	originPointCongestionWindow protocol.ByteCount

	// Time to origin point of cubic function in 2^10 fractions of a second.
	timeToOriginPoint uint32

	// Last congestion window in packets computed by cubic function.
	lastTargetCongestionWindow protocol.ByteCount
}

// NewCubic returns a new Cubic instance
func NewCubic(clock Clock) *Cubic {
	c := &Cubic{
		clock:          clock,
		numConnections: defaultNumConnections,
		epochCounter:   0,
	}
	c.Reset()
	return c
}

// Reset is called after a timeout to reset the cubic state
func (c *Cubic) Reset() {
	c.epoch = time.Time{}
	c.epochCounter = 0
	c.lastMaxCongestionWindow = 0
	c.ackedBytesCount = 0
	c.estimatedTCPcongestionWindow = 0
	c.originPointCongestionWindow = 0
	c.timeToOriginPoint = 0
	c.lastTargetCongestionWindow = 0
}

func (c *Cubic) alpha() float32 {
	// TCPFriendly alpha is described in Section 3.3 of the CUBIC paper. Note that
	// beta here is a cwnd multiplier, and is equal to 1-beta from the paper.
	// We derive the equivalent alpha for an N-connection emulation as:
	b := c.beta()
	return 3 * float32(c.numConnections) * float32(c.numConnections) * (1 - b) / (1 + b)
}

func (c *Cubic) beta() float32 {
	// kNConnectionBeta is the backoff factor after loss for our N-connection
	// emulation, which emulates the effective backoff of an ensemble of N
	// TCP-Reno connections on a single loss event. The effective multiplier is
	// computed as:
	return (float32(c.numConnections) - 1 + beta) / float32(c.numConnections)
}

func (c *Cubic) betaLastMax() float32 {
	// betaLastMax is the additional backoff factor after loss for our
	// N-connection emulation, which emulates the additional backoff of
	// an ensemble of N TCP-Reno connections on a single loss event. The
	// effective multiplier is computed as:
	return (float32(c.numConnections) - 1 + betaLastMax) / float32(c.numConnections)
}

// OnApplicationLimited is called on ack arrival when sender is unable to use
// the available congestion window. Resets Cubic state during quiescence.
func (c *Cubic) OnApplicationLimited() {
	// When sender is not using the available congestion window, the window does
	// not grow. But to be RTT-independent, Cubic assumes that the sender has been
	// using the entire window during the time since the beginning of the current
	// "epoch" (the end of the last loss recovery period). Since
	// application-limited periods break this assumption, we reset the epoch when
	// in such a period. This reset effectively freezes congestion window growth
	// through application-limited periods and allows Cubic growth to continue
	// when the entire window is being used.
	c.epoch = time.Time{}
}

// CongestionWindowAfterPacketLoss computes a new congestion window to use after
// a loss event. Returns the new congestion window in packets. The new
// congestion window is a multiplicative decrease of our current window.
func (c *Cubic) CongestionWindowAfterPacketLoss(currentCongestionWindow protocol.ByteCount) protocol.ByteCount {
	if currentCongestionWindow+protocol.DefaultTCPMSS < c.lastMaxCongestionWindow {
		// We never reached the old max, so assume we are competing with another
		// flow. Use our extra back off factor to allow the other flow to go up.
		c.lastMaxCongestionWindow = protocol.ByteCount(c.betaLastMax() * float32(currentCongestionWindow))
	} else {
		c.lastMaxCongestionWindow = currentCongestionWindow
	}
	c.epoch = time.Time{} // Reset time.
	return protocol.ByteCount(float32(currentCongestionWindow) * c.beta())
}

// CongestionWindowAfterAck computes a new congestion window to use after a received ACK.
// Returns the new congestion window in packets. The new congestion window
// follows a cubic function that depends on the time passed since last
// packet loss.
func (c *Cubic) CongestionWindowAfterAck(
	ackedBytes protocol.ByteCount,
	currentCongestionWindow protocol.ByteCount,
	delayMin time.Duration,
	eventTime time.Time,
	rtt time.Duration,
) protocol.ByteCount {
	c.ackedBytesCount += ackedBytes
	currentTime := time.Now()
	timeSinceEpoch := currentTime.Sub(c.epoch)
	if int(timeSinceEpoch) > (int(rtt) / 1000000) {
		log.Printf("Epoch ENDED: %s, currentTime: %s, timeSinceEpoch: %d\n", c.epoch, currentTime, int(timeSinceEpoch)/1000000)
		c.epoch = currentTime
		c.epochCounter += 1
	}
	if c.epochCounter == 10 {
		c.epochCounter = 0
		log.Printf("DO THE TESTS NOW NOW NOW NOW\n")
	}
	log.Printf("Epoch: %s, currentTime: %s, timeSinceEpoch: %d, refined time: %d\n", c.epoch, currentTime, currentTime.Sub(c.epoch)/time.Microsecond, int(timeSinceEpoch)/1000000000)
	if c.epoch.IsZero() {
		// First ACK after a loss event.
		c.epoch = eventTime            // Start of epoch.
		c.ackedBytesCount = ackedBytes // Reset count.
		// Reset estimated_tcp_congestion_window_ to be in sync with cubic.
		c.estimatedTCPcongestionWindow = currentCongestionWindow
		if c.lastMaxCongestionWindow <= currentCongestionWindow {
			c.timeToOriginPoint = 0
			c.originPointCongestionWindow = currentCongestionWindow
		} else {
			c.timeToOriginPoint = uint32(math.Cbrt(float64(cubeFactor * (c.lastMaxCongestionWindow - currentCongestionWindow))))
			c.originPointCongestionWindow = c.lastMaxCongestionWindow
		}
	}

	// Change the time unit from microseconds to 2^10 fractions per second. Take
	// the round trip time in account. This is done to allow us to use shift as a
	// divide operator.
	elapsedTime := int64(eventTime.Add(delayMin).Sub(c.epoch)/time.Microsecond) << 10 / (1000 * 1000)

	// Right-shifts of negative, signed numbers have implementation-dependent
	// behavior, so force the offset to be positive, as is done in the kernel.
	offset := int64(c.timeToOriginPoint) - elapsedTime
	if offset < 0 {
		offset = -offset
	}

	deltaCongestionWindow := protocol.ByteCount(cubeCongestionWindowScale*offset*offset*offset) * protocol.DefaultTCPMSS >> cubeScale
	var targetCongestionWindow protocol.ByteCount
	if elapsedTime > int64(c.timeToOriginPoint) {
		targetCongestionWindow = c.originPointCongestionWindow + deltaCongestionWindow
	} else {
		targetCongestionWindow = c.originPointCongestionWindow - deltaCongestionWindow
	}
	// Limit the CWND increase to half the acked bytes.
	targetCongestionWindow = utils.MinByteCount(targetCongestionWindow, currentCongestionWindow+c.ackedBytesCount/2)

	// Increase the window by approximately Alpha * 1 MSS of bytes every
	// time we ack an estimated tcp window of bytes.  For small
	// congestion windows (less than 25), the formula below will
	// increase slightly slower than linearly per estimated tcp window
	// of bytes.
	c.estimatedTCPcongestionWindow += protocol.ByteCount(float32(c.ackedBytesCount) * c.alpha() * float32(protocol.DefaultTCPMSS) / float32(c.estimatedTCPcongestionWindow))
	c.ackedBytesCount = 0

	// We have a new cubic congestion window.
	c.lastTargetCongestionWindow = targetCongestionWindow

	// Compute target congestion_window based on cubic target and estimated TCP
	// congestion_window, use highest (fastest).
	if targetCongestionWindow < c.estimatedTCPcongestionWindow {
		//targetCongestionWindow = c.estimatedTCPcongestionWindow
	}
	return targetCongestionWindow
}

// SetNumConnections sets the number of emulated connections
func (c *Cubic) SetNumConnections(n int) {
	c.numConnections = n
} */

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
