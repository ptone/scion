import type { AgentRing } from './agents';

const BEAM_CHARGE_DURATION = 300; // ms for beam to charge up
const BEAM_TRAVEL_DURATION = 400; // ms for beam to travel
const BEAM_LINGER_DURATION = 600; // ms for beam to linger and fade
const TOTAL_BEAM_DURATION = BEAM_CHARGE_DURATION + BEAM_TRAVEL_DURATION + BEAM_LINGER_DURATION;

interface ActiveBeam {
  fromAgent: string;
  toAgent: string;
  startTime: number;
  fromColor: string;
  // Captured positions at beam start (frozen)
  frozenFrom: { x: number; y: number };
  frozenTo: { x: number; y: number };
}

export class DestroyBeamRenderer {
  private beams: ActiveBeam[] = [];
  private agentRing: AgentRing | null = null;

  setAgentRing(ring: AgentRing): void {
    this.agentRing = ring;
  }

  addBeam(
    requestedByName: string,
    targetName: string,
    agentRing: AgentRing
  ): void {
    const fromPos = agentRing.getAgentPosition(requestedByName);
    const toPos = agentRing.getAgentPosition(targetName);
    if (!fromPos || !toPos) return;

    // Freeze ring so target doesn't move during beam travel
    agentRing.freezeRebalance();

    this.beams.push({
      fromAgent: requestedByName,
      toAgent: targetName,
      startTime: Date.now(),
      fromColor: agentRing.getAgentColor(requestedByName),
      frozenFrom: { ...fromPos },
      frozenTo: { ...toPos },
    });
  }

  reset(): void {
    this.beams = [];
  }

  draw(ctx: CanvasRenderingContext2D): void {
    const now = Date.now();

    this.beams = this.beams.filter((b) => {
      const expired = now - b.startTime >= TOTAL_BEAM_DURATION;
      if (expired && this.agentRing) {
        // Unfreeze ring when beam completes
        this.agentRing.unfreezeRebalance();
      }
      return !expired;
    });

    for (const beam of this.beams) {
      const elapsed = now - beam.startTime;
      const { frozenFrom: from, frozenTo: to } = beam;

      if (elapsed < BEAM_CHARGE_DURATION) {
        // Charge-up phase: pulsing glow at source
        const t = elapsed / BEAM_CHARGE_DURATION;
        const pulseAlpha = 0.3 + 0.7 * Math.sin(t * Math.PI * 4);

        ctx.save();
        ctx.globalAlpha = pulseAlpha * t;
        ctx.shadowBlur = 20;
        ctx.shadowColor = '#ff0000';
        ctx.beginPath();
        ctx.arc(from.x, from.y, 8 + 6 * t, 0, Math.PI * 2);
        ctx.fillStyle = '#ff3333';
        ctx.fill();
        ctx.restore();
      } else if (elapsed < BEAM_CHARGE_DURATION + BEAM_TRAVEL_DURATION) {
        // Beam travel phase: red laser zapping from source to target
        const travelElapsed = elapsed - BEAM_CHARGE_DURATION;
        const t = travelElapsed / BEAM_TRAVEL_DURATION;
        const eased = 1 - Math.pow(1 - t, 2); // ease-out quad

        const beamEndX = from.x + (to.x - from.x) * eased;
        const beamEndY = from.y + (to.y - from.y) * eased;

        // Core beam (bright red, thin)
        ctx.save();
        ctx.shadowBlur = 15;
        ctx.shadowColor = '#ff0000';

        // Outer glow
        ctx.beginPath();
        ctx.moveTo(from.x, from.y);
        ctx.lineTo(beamEndX, beamEndY);
        ctx.strokeStyle = 'rgba(255, 0, 0, 0.3)';
        ctx.lineWidth = 8;
        ctx.stroke();

        // Mid glow
        ctx.beginPath();
        ctx.moveTo(from.x, from.y);
        ctx.lineTo(beamEndX, beamEndY);
        ctx.strokeStyle = 'rgba(255, 50, 50, 0.6)';
        ctx.lineWidth = 4;
        ctx.stroke();

        // Core beam
        ctx.beginPath();
        ctx.moveTo(from.x, from.y);
        ctx.lineTo(beamEndX, beamEndY);
        ctx.strokeStyle = 'rgba(255, 150, 150, 0.9)';
        ctx.lineWidth = 2;
        ctx.stroke();

        // Beam tip glow
        ctx.beginPath();
        ctx.arc(beamEndX, beamEndY, 6, 0, Math.PI * 2);
        ctx.fillStyle = 'rgba(255, 100, 100, 0.8)';
        ctx.fill();

        // Electrical crackle particles along the beam
        const crackleCount = Math.floor(t * 8);
        for (let i = 0; i < crackleCount; i++) {
          const ct = Math.random() * eased;
          const cx = from.x + (to.x - from.x) * ct;
          const cy = from.y + (to.y - from.y) * ct;
          const offset = (Math.random() - 0.5) * 12;
          const dx = to.y - from.y;
          const dy = -(to.x - from.x);
          const len = Math.sqrt(dx * dx + dy * dy) || 1;
          ctx.beginPath();
          ctx.arc(cx + (dx / len) * offset, cy + (dy / len) * offset, 1.5, 0, Math.PI * 2);
          ctx.fillStyle = `rgba(255, 200, 200, ${0.5 + Math.random() * 0.5})`;
          ctx.fill();
        }

        ctx.restore();
      } else {
        // Linger phase: full beam fading out with impact effect
        const lingerElapsed = elapsed - BEAM_CHARGE_DURATION - BEAM_TRAVEL_DURATION;
        const t = lingerElapsed / BEAM_LINGER_DURATION;
        const alpha = 1 - t;

        ctx.save();
        ctx.globalAlpha = alpha;
        ctx.shadowBlur = 10 * alpha;
        ctx.shadowColor = '#ff0000';

        // Fading beam
        ctx.beginPath();
        ctx.moveTo(from.x, from.y);
        ctx.lineTo(to.x, to.y);
        ctx.strokeStyle = `rgba(255, 50, 50, ${0.6 * alpha})`;
        ctx.lineWidth = 3 * alpha;
        ctx.stroke();

        // Impact explosion at target
        const impactR = 10 + 20 * t;
        ctx.beginPath();
        ctx.arc(to.x, to.y, impactR, 0, Math.PI * 2);
        ctx.strokeStyle = `rgba(255, 50, 0, ${0.8 * alpha})`;
        ctx.lineWidth = 2 * (1 - t);
        ctx.stroke();

        // Inner impact flash
        if (t < 0.3) {
          const flashAlpha = (1 - t / 0.3) * alpha;
          ctx.beginPath();
          ctx.arc(to.x, to.y, 8 * (1 - t), 0, Math.PI * 2);
          ctx.fillStyle = `rgba(255, 200, 100, ${flashAlpha})`;
          ctx.fill();
        }

        ctx.restore();
      }
    }
  }
}
