import type { Messages } from '../en';
import { common } from './common';
import { create } from './create';
import { credentials } from './credentials';
import { errors } from './errors';
import { guides } from './guides';
import { live } from './live';
import { manage } from './manage';
import { open } from './open';
import { request } from './request';

export const ja: Messages = {
	common,
	create,
	open,
	manage,
	request,
	live,
	credentials,
	errors,
	guides
};
