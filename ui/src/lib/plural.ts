/** "1 type", "6 types". */
export const count = (n: number, noun: string) => `${n} ${noun}${n === 1 ? '' : 's'}`
