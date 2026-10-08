import { afterEach, describe, expect, it } from 'vitest';
import {
	clearCreatedDelivery,
	handOffCreatedDelivery,
	takeCreatedDelivery
} from './created-delivery';

const created = {
	id: 'fixture',
	token: 'private-capability',
	recipientUrl: 'https://flick.example.test/s/fixture#key=recipient-key',
	usesPassphrase: false
};
afterEach(clearCreatedDelivery);

describe('memory-only creation handoff', () => {
	it('can be consumed only once for the matching delivery and capability', () => {
		handOffCreatedDelivery(created);
		expect(takeCreatedDelivery(created.id, created.token)).toEqual(created);
		expect(takeCreatedDelivery(created.id, created.token)).toBeNull();
	});
	it('a different management capability cannot take a recipient key', () => {
		handOffCreatedDelivery(created);
		expect(takeCreatedDelivery(created.id, 'another-token')).toBeNull();
		expect(takeCreatedDelivery(created.id, created.token)).toBeNull();
	});
	it('keeps only the current handoff and clears it on leaving the flow', () => {
		handOffCreatedDelivery(created);
		handOffCreatedDelivery({ ...created, id: 'next' });
		expect(takeCreatedDelivery(created.id, created.token)).toBeNull();
		handOffCreatedDelivery(created);
		clearCreatedDelivery();
		expect(takeCreatedDelivery(created.id, created.token)).toBeNull();
	});
});
