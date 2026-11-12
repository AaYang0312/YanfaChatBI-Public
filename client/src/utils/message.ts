
// Formats to 15 Mar 2025, 12:00
export const formatActionAt = (actionAt: string) => {
	const date = new Date(actionAt);
	if (isNaN(date.getTime())) {
		return "";
	}
	// Format to 15 Mar 2025, 12:00
	return date.toLocaleString('en-US', {
		month: 'short',
		day: 'numeric',
		year: 'numeric',
		hour: '2-digit',
		minute: '2-digit',
	});
};

const responseError = (error: unknown): string | null => {
	if (error === null || typeof error !== 'object') return null;
	if (!('response' in error) || error.response === null || typeof error.response !== 'object') return null;
	const response = error.response;
	if (!('data' in response) || response.data === null || typeof response.data !== 'object') return null;
	const data = response.data;
	if (!('error' in data) || typeof data.error !== 'string' || !data.error) return null;
	return data.error;
};

/** Response `data.error`, otherwise the fallback — the precedence most service catches used before typing. */
export const getResponseErrorMessage = (error: unknown, fallback: string): string =>
	responseError(error) ?? fallback;

/** Response `data.error`, then the error's own message, otherwise the fallback. */
export const getErrorMessage = (error: unknown, fallback: string): string => {
	if (error !== null && typeof error === 'object') {
		const fromResponse = responseError(error);
		if (fromResponse) return fromResponse;
		if ('message' in error && typeof error.message === 'string' && error.message) return error.message;
	}
	return fallback;
};

