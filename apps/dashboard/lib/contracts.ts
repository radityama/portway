import type { User, Organization, Role } from '../../api/src/models';
export type {
  Project,
  Tunnel,
  Domain,
  Relay,
  Role,
} from '../../api/src/models';
export type Profile = { user: User; organization: Organization; role: Role };
export type Proof = { type: 'TXT'; name: string; value: string };
