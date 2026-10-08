import type { PageLoad } from './$types';

export const prerender = false;
export const load: PageLoad = ({ params }) => ({ secretId: params.id });
