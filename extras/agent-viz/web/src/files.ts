import type { FileEditEvent } from './types';
import type { AgentRing } from './agents';
import type { FileGraph } from './graph';

const PARTICLE_DURATION_CREATE = 1000; // ms for create particle to travel (slower, more dramatic)
const PARTICLE_DURATION_EDIT = 600; // ms for edit particle to travel
const PARTICLE_DURATION_READ = 600; // ms for read particle to travel
const MATERIALIZE_DURATION = 600; // ms for new file to appear

interface ActiveParticle {
  fromAgentId: string;
  filePath: string;
  color: string;
  action: 'create' | 'edit' | 'read';
  startTime: number;
  revealed: boolean; // whether we've triggered file reveal
}

export class FileEditRenderer {
  private particles: ActiveParticle[] = [];
  private fileGraph: FileGraph | null = null;
  private agentRing: AgentRing | null = null;

  setFileGraph(fg: FileGraph): void {
    this.fileGraph = fg;
  }

  setAgentRing(ring: AgentRing): void {
    this.agentRing = ring;
  }

  addFileEdit(
    event: FileEditEvent,
    agentRing: AgentRing,
    fileGraph: FileGraph
  ): void {
    const agentPos = agentRing.getAgentPosition(event.agentId);
    if (!agentPos) return;

    const color = agentRing.getAgentColor(event.agentId);

    this.particles.push({
      fromAgentId: event.agentId,
      filePath: event.filePath,
      color,
      action: event.action,
      startTime: Date.now(),
      revealed: false,
    });

    // Highlight the file in the graph (only if already visible)
    if (event.action !== 'create') {
      fileGraph.highlightFile(event.filePath);
    }
  }

  /** Resolve the current screen position for a file node (walks up to nearest parent if needed). */
  private resolveFileScreenPos(filePath: string): { x: number; y: number } | null {
    if (!this.fileGraph) return null;
    const graph = this.fileGraph.getGraph();

    let pos = this.fileGraph.getNodePosition(filePath);
    if (!pos) {
      let parent = filePath;
      while (parent.includes('/')) {
        parent = parent.substring(0, parent.lastIndexOf('/'));
        pos = this.fileGraph.getNodePosition(parent);
        if (pos) break;
      }
    }
    if (!pos) return null;
    return graph.graph2ScreenCoords(pos.x, pos.y);
  }

  reset(): void {
    this.particles = [];
  }

  draw(ctx: CanvasRenderingContext2D): void {
    const now = Date.now();

    this.particles = this.particles.filter((p) => {
      const travelDuration = this.getTravelDuration(p);
      const totalDuration =
        p.action === 'create' ? travelDuration + MATERIALIZE_DURATION : travelDuration;
      return now - p.startTime < totalDuration;
    });

    for (const particle of this.particles) {
      const elapsed = now - particle.startTime;
      const travelDuration = this.getTravelDuration(particle);

      // Resolve positions dynamically each frame so we track the node as it settles
      const agentPos = this.agentRing?.getAgentPosition(particle.fromAgentId);
      const fileScreenPos = this.resolveFileScreenPos(particle.filePath);
      if (!agentPos || !fileScreenPos) continue;

      const isRead = particle.action === 'read';
      const from = isRead ? fileScreenPos : agentPos;
      const to = isRead ? agentPos : fileScreenPos;

      // Trigger file reveal when create particle arrives at destination
      if (
        particle.action === 'create' &&
        !particle.revealed &&
        elapsed >= travelDuration
      ) {
        particle.revealed = true;
        if (this.fileGraph) {
          this.fileGraph.revealFile(particle.filePath);
          this.fileGraph.highlightFile(particle.filePath);
        }
      }

      if (elapsed < travelDuration) {
        // Particle traveling
        const t = elapsed / travelDuration;
        // Ease-out cubic
        const eased = 1 - Math.pow(1 - t, 3);
        const x = from.x + (to.x - from.x) * eased;
        const y = from.y + (to.y - from.y) * eased;

        if (particle.action === 'read') {
          // Read: clear trail from file back to agent
          const trailLength = 5;
          for (let i = 0; i < trailLength; i++) {
            const tt = Math.max(0, eased - i * 0.035);
            const tx = from.x + (to.x - from.x) * tt;
            const ty = from.y + (to.y - from.y) * tt;
            ctx.beginPath();
            ctx.arc(tx, ty, 3 - i * 0.4, 0, Math.PI * 2);
            ctx.fillStyle = this.getColor(particle, 0.7 - i * 0.11);
            ctx.fill();
          }
          ctx.beginPath();
          ctx.arc(x, y, 4, 0, Math.PI * 2);
          ctx.fillStyle = this.getColor(particle, 0.9);
          ctx.shadowBlur = 14;
          ctx.shadowColor = particle.color;
          ctx.fill();
          ctx.shadowBlur = 0;
        } else if (particle.action === 'create') {
          // Create: large, bright, prominent comet trail
          const trailLength = 8;
          for (let i = 0; i < trailLength; i++) {
            const tt = Math.max(0, eased - i * 0.025);
            const tx = from.x + (to.x - from.x) * tt;
            const ty = from.y + (to.y - from.y) * tt;
            ctx.beginPath();
            ctx.arc(tx, ty, 5 - i * 0.5, 0, Math.PI * 2);
            ctx.fillStyle = this.getColor(particle, 0.9 - i * 0.1);
            ctx.fill();
          }
          // Bright leading particle with strong glow
          ctx.beginPath();
          ctx.arc(x, y, 7, 0, Math.PI * 2);
          ctx.fillStyle = '#fff';
          ctx.shadowBlur = 25;
          ctx.shadowColor = particle.color;
          ctx.fill();
          ctx.shadowBlur = 0;
          // Colored core
          ctx.beginPath();
          ctx.arc(x, y, 5, 0, Math.PI * 2);
          ctx.fillStyle = this.getColor(particle, 1);
          ctx.fill();
        } else {
          // Edit: moderate trail
          const trailLength = 5;
          for (let i = 0; i < trailLength; i++) {
            const tt = Math.max(0, eased - i * 0.03);
            const tx = from.x + (to.x - from.x) * tt;
            const ty = from.y + (to.y - from.y) * tt;
            ctx.beginPath();
            ctx.arc(tx, ty, 3 - i * 0.5, 0, Math.PI * 2);
            ctx.fillStyle = this.getColor(particle, 0.8 - i * 0.15);
            ctx.fill();
          }
          ctx.beginPath();
          ctx.arc(x, y, 4, 0, Math.PI * 2);
          ctx.fillStyle = this.getColor(particle, 1);
          ctx.shadowBlur = 12;
          ctx.shadowColor = particle.color;
          ctx.fill();
          ctx.shadowBlur = 0;
        }
      } else if (particle.action === 'create') {
        // Materialize effect — expanding bright ring + flash at destination
        const mt = (elapsed - travelDuration) / MATERIALIZE_DURATION;
        const alpha = 1 - mt;

        ctx.save();

        // Inner flash (bright white→color fade)
        if (mt < 0.4) {
          const flashAlpha = (1 - mt / 0.4) * 0.8;
          ctx.globalAlpha = flashAlpha;
          ctx.beginPath();
          ctx.arc(to.x, to.y, 12 * (0.5 + mt), 0, Math.PI * 2);
          ctx.fillStyle = '#fff';
          ctx.shadowBlur = 20;
          ctx.shadowColor = particle.color;
          ctx.fill();
          ctx.shadowBlur = 0;
        }

        // Expanding ring
        ctx.globalAlpha = alpha;
        const ringScale = Math.min(1, mt * 1.2);
        ctx.beginPath();
        ctx.arc(to.x, to.y, 15 * ringScale, 0, Math.PI * 2);
        ctx.strokeStyle = this.getColor(particle, 0.9);
        ctx.lineWidth = 2.5 * (1 - mt);
        ctx.stroke();

        ctx.restore();
      }
    }
  }

  private getTravelDuration(particle: ActiveParticle): number {
    switch (particle.action) {
      case 'create':
        return PARTICLE_DURATION_CREATE;
      case 'read':
        return PARTICLE_DURATION_READ;
      default:
        return PARTICLE_DURATION_EDIT;
    }
  }

  private getColor(particle: ActiveParticle, alpha: number): string {
    const hex = particle.color;
    const r = parseInt(hex.slice(1, 3), 16);
    const g = parseInt(hex.slice(3, 5), 16);
    const b = parseInt(hex.slice(5, 7), 16);
    return `rgba(${r}, ${g}, ${b}, ${alpha})`;
  }
}
