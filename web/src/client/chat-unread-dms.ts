/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/**
 * The rail's Unread DMs list: every DM the unread badge counts, so each one
 * can be found and opened from the rail, whichever space its peer belongs
 * to and even when the peer agent has been deleted.
 *
 * The input is `GET /api/v1/chat/dms`. A DM is unread exactly when the
 * badge (`GET /api/v1/chat/unread-count`) counts it: `hasUnread` is set and
 * it is not muted. Both come from the same server rule (latest message past
 * the caller's read watermark), so the list length and the badge's `dms`
 * field agree.
 */

/** One entry of `GET /api/v1/chat/dms`, as far as the rail is concerned. */
export interface ChatDMListEntry {
  conversationKey: string;
  peerId: string;
  peerKind: string;
  peerName?: string;
  peerSlug?: string;
  hasUnread?: boolean;
  muted?: boolean;
}

/** A row of the rail's Unread DMs list. */
export interface ChatRailDM {
  conversationKey: string;
  peerId: string;
  peerKind: 'user' | 'agent';
  displayName: string;
  /** Whether the badge counts it; false only for the kept open DM. */
  unread: boolean;
}

/** Whether the unread badge counts dm. */
export function isBadgeUnreadDM(dm: ChatDMListEntry): boolean {
  return dm.hasUnread === true && dm.muted !== true;
}

/**
 * The name to show for a DM peer. A deleted agent keeps its name (the hub
 * resolves deleted agents too); a peer the hub cannot resolve at all still
 * gets a row, so the DM stays reachable.
 */
export function dmPeerDisplayName(dm: ChatDMListEntry): string {
  const name = dm.peerName?.trim() || dm.peerSlug?.trim();
  if (name) return name;
  return dm.peerKind === 'agent' ? 'Unknown agent' : 'Unknown user';
}

/**
 * The rows of the Unread DMs list, in the order the hub returned them (most
 * recent activity first): every DM the badge counts, plus the open DM
 * (selectedKey) even once read, so reading it does not pull it out from
 * under the user, as the rail does for the open thread.
 */
export function railUnreadDMs(dms: readonly ChatDMListEntry[], selectedKey = ''): ChatRailDM[] {
  const rows: ChatRailDM[] = [];
  for (const dm of dms) {
    const unread = isBadgeUnreadDM(dm);
    if (!unread && (!selectedKey || dm.conversationKey !== selectedKey)) continue;
    rows.push({
      conversationKey: dm.conversationKey,
      peerId: dm.peerId,
      peerKind: dm.peerKind === 'agent' ? 'agent' : 'user',
      displayName: dmPeerDisplayName(dm),
      unread,
    });
  }
  return rows;
}

/**
 * The unread badge's number from the rail's own lists: the sum of the
 * spaces' unread rollups (`GET /api/v1/chat/spaces`) plus the DMs the badge
 * counts (`GET /api/v1/chat/dms`). It equals `conversations` from `GET
 * /api/v1/chat/unread-count`, which the hub computes from the same rollup
 * and DM rule. Returns null when either body is missing.
 */
export function badgeCountFromLists(
  spaces: { spaces?: unknown[] } | null,
  dms: { dms?: unknown[] } | null
): number | null {
  if (!spaces || !dms) return null;
  let n = 0;
  for (const sp of spaces.spaces ?? []) {
    const c = (sp as { unreadCount?: unknown } | null)?.unreadCount;
    if (typeof c === 'number' && Number.isFinite(c) && c > 0) n += Math.floor(c);
  }
  for (const dm of dms.dms ?? []) {
    if (dm && isBadgeUnreadDM(dm as ChatDMListEntry)) n++;
  }
  return n;
}
