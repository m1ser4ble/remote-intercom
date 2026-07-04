export interface PendingAsk {
  id: string;
  from?: string;
  to?: string;
  channelId?: string;
  message: string;
  receivedAt: string;
}

export interface PendingJoinRequest {
  id: string;
  joinRequestId: string;
  deviceId: string;
  deviceName: string;
  channelId?: string;
  channelName?: string;
  receivedAt: string;
}

export interface PendingSnapshot {
  asks: PendingAsk[];
  joinRequests: PendingJoinRequest[];
}

export class PendingState {
  private readonly asks = new Map<string, PendingAsk>();
  private readonly joinRequests = new Map<string, PendingJoinRequest>();

  addAsk(ask: PendingAsk): PendingAsk {
    this.asks.set(ask.id, ask);
    return ask;
  }

  getAsk(id: string): PendingAsk | undefined {
    return this.asks.get(id);
  }

  deleteAsk(id: string): boolean {
    return this.asks.delete(id);
  }

  listAsks(): PendingAsk[] {
    return [...this.asks.values()];
  }

  addJoinRequest(joinRequest: PendingJoinRequest): PendingJoinRequest {
    this.joinRequests.set(joinRequest.id, joinRequest);
    return joinRequest;
  }

  getJoinRequest(id: string): PendingJoinRequest | undefined {
    return this.joinRequests.get(id);
  }

  deleteJoinRequest(id: string): boolean {
    return this.joinRequests.delete(id);
  }

  listJoinRequests(): PendingJoinRequest[] {
    return [...this.joinRequests.values()];
  }

  snapshot(): PendingSnapshot {
    return {
      asks: this.listAsks(),
      joinRequests: this.listJoinRequests(),
    };
  }

  clear(): void {
    this.asks.clear();
    this.joinRequests.clear();
  }
}

export function createPendingState(): PendingState {
  return new PendingState();
}
