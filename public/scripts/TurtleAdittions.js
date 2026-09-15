function replaceCyrillic(input) {
    const cyrillicMap = {
        'А': 'A', 'Е': 'E', 'О': 'O',
        'а': 'a', 'е': 'e', 'о': 'o',
    };


    let output = input.replace(/[АЕОаео]/g, function(match) {
        return cyrillicMap[match] || match;
    });

    return output;
}

function replaceVowels(input) {
    const vowelMap = {
        'A': 'А', 'E': 'Е', 'O': 'О',
        'a': 'а', 'e': 'е', 'o': 'о',
    };


    let output = input.replace(/[AEIOUYaeiouy]/g, function(match) {
        return vowelMap[match] || match;
    });

    return output;
}

export { replaceCyrillic, replaceVowels };
